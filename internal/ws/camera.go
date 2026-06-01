package ws

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"log"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/image/draw"
)

type CameraStreamer struct {
	hub        *Hub
	imagePath  string
	outWidth   float64
	outHeight  float64
	mu         sync.Mutex
	currentB64 string
	dstBuf     *image.RGBA
	jpegBuf    bytes.Buffer
}

func NewCameraStreamer(hub *Hub, imagePath string, width, height float64) *CameraStreamer {
	return &CameraStreamer{
		hub:       hub,
		imagePath: imagePath,
		outWidth:  width,
		outHeight: height,
	}
}

func (c *CameraStreamer) Start() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := watcher.Add(c.imagePath); err != nil {
		watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					c.processAndBroadcast()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("camera watcher error: %v", err)
			}
		}
	}()

	c.processAndBroadcast()
	return nil
}

func (c *CameraStreamer) processAndBroadcast() {
	img, err := c.readImage()
	if err != nil {
		log.Printf("camera: read image: %v", err)
		return
	}

	shrunk := c.shrinkImage(img)
	b64 := c.imageToBase64(shrunk)
	if b64 == "" {
		log.Printf("camera: encode image to base64 failed")
		return
	}

	c.mu.Lock()
	c.currentB64 = b64
	c.mu.Unlock()

	c.hub.Broadcast([]byte("[CAMERA]" + b64))
}

func (c *CameraStreamer) readImage() (image.Image, error) {
	f, err := os.Open(c.imagePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}

func (c *CameraStreamer) GetCurrentFrame() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentB64
}

func (c *CameraStreamer) shrinkImage(img image.Image) image.Image {
	bounds := img.Bounds()
	w := float64(bounds.Dx())
	h := float64(bounds.Dy())

	scaleX := c.outWidth / w
	scaleY := c.outHeight / h
	scale := scaleX
	if scaleY < scale {
		scale = scaleY
	}

	if scale >= 1 {
		return img
	}

	newW := int(w * scale)
	newH := int(h * scale)
	if c.dstBuf == nil || c.dstBuf.Bounds().Dx() != newW || c.dstBuf.Bounds().Dy() != newH {
		c.dstBuf = image.NewRGBA(image.Rect(0, 0, newW, newH))
	}
	draw.ApproxBiLinear.Scale(c.dstBuf, c.dstBuf.Bounds(), img, img.Bounds(), draw.Over, nil)
	return c.dstBuf
}

func (c *CameraStreamer) imageToBase64(img image.Image) string {
	c.jpegBuf.Reset()
	if err := jpeg.Encode(&c.jpegBuf, img, nil); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(c.jpegBuf.Bytes())
}
