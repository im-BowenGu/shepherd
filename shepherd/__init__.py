import errno
import os
from pathlib import Path
import logging
from logging.handlers import SysLogHandler
from flask import Flask, render_template, send_file
from flask_sockets import Sockets
from flask_cors import CORS
import RPi.GPIO as GPIO
from PIL import Image
import base64

from shepherd.blueprints import upload, run, pyls, editor, staticroutes

# Constants
START_BUTTON_PIN = 26  # BCM pin number
USER_CODE_PATH = os.path.join(os.getcwd(), "usercode")
USER_CODE_ENTRYPOINT_NAME = "main.py"
USER_CODE_ENTRYPOINT_PATH = os.path.join(USER_CODE_PATH, USER_CODE_ENTRYPOINT_NAME)
GAME_CONTROL_PATH = Path('/media/ArenaUSB')
TEAMNAME_FILE = Path('/home/pi/teamname.txt')
STATIC_GRAPHIC_PATH = Path('shepherd/static/image.jpg')
TEMP_IMAGE_PATH = Path('/tmp/current.jpg')

# Logging setup
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)
logger.addHandler(SysLogHandler('/dev/log'))

# Flask app setup
app = Flask(__name__, template_folder="templates")
sockets = Sockets(app)
CORS(app, resources=r'/*')
app.secret_key = os.urandom(32)
app.config.update(
    SEND_FILE_MAX_AGE_DEFAULT=0,
    MAX_CONTENT_LENGTH=64 * 1024 * 1024,  # 64 MiB
    SHEPHERD_USER_CODE_PATH=USER_CODE_PATH,
    SHEPHERD_USER_CODE_ENTRYPOINT_NAME=USER_CODE_ENTRYPOINT_NAME,
    SHEPHERD_USER_CODE_ENTRYPOINT_PATH=USER_CODE_ENTRYPOINT_PATH
)

# Ensure user code directory exists
try:
    os.makedirs(USER_CODE_PATH, exist_ok=True)
except OSError as e:
    if e.errno != errno.EEXIST:
        logger.error(f"Failed to create user code directory: {e}")
        raise

def setup_gpio():
    """Initialize GPIO settings for the start button."""
    GPIO.setmode(GPIO.BCM)
    GPIO.setup(START_BUTTON_PIN, GPIO.IN, pull_up_down=GPIO.PUD_UP)
    GPIO.add_event_detect(START_BUTTON_PIN, GPIO.FALLING, callback=start_callback, bouncetime=3000)

def get_team_image():
    """Determine and process the appropriate start graphic for the team."""
    teamname_jpg = (TEAMNAME_FILE.read_text().strip() + '.jpg') if TEAMNAME_FILE.exists() else 'none'
    graphic_paths = [
        GAME_CONTROL_PATH / teamname_jpg,
        Path('robotsrc/team_logo.jpg'),
        GAME_CONTROL_PATH / 'Corner.jpg',
        Path('/home/pi/game_logo.jpg')
    ]
    
    for graphic_path in graphic_paths:
        if graphic_path.exists():
            try:
                STATIC_GRAPHIC_PATH.write_bytes(graphic_path.read_bytes())
                with Image.open(graphic_path) as img:
                    img = img.resize((1280, 720), Image.Resampling.LANCZOS)
                    img.save(TEMP_IMAGE_PATH, quality=85)
                return True
            except Exception as e:
                logger.warning(f"Failed to process image {graphic_path}: {e}")
    logger.warning("No valid start graphic found")
    return False

def start_callback(channel):
    """Callback function for start button press."""
    zone = "0"
    for i in range(1, 4):
        if (GAME_CONTROL_PATH / f'zone{i}.txt').exists():
            zone = str(i)
            break
    
    with app.test_request_context(data={"zone": zone, "mode": "competition"}):
        run.start()

# Initialize app and GPIO
if not app.debug or os.environ.get("WERKZEUG_RUN_MAIN") == "true":
    run.init(app)
    setup_gpio()
    get_team_image()

# Register blueprints
app.register_blueprint(upload.blueprint, url_prefix="/upload")
app.register_blueprint(run.blueprint, url_prefix="/run")
app.register_blueprint(editor.blueprint, url_prefix="/files")
app.register_blueprint(staticroutes.blueprint, url_prefix="/")
sockets.register_blueprint(pyls.blueprint)

# Routes
@app.route("/")
def index():
    return render_template("index.html")

@app.route("/about")
def about():
    return render_template("about.html")

@app.route("/favicon.ico")
def favicon():
    return send_file(os.path.join(app.root_path, "static", "favicon.ico"))

@app.route("/livestream")
def livestream():
    try:
        with TEMP_IMAGE_PATH.open("rb") as image:
            return base64.b85encode(image.read()).decode()
    except FileNotFoundError:
        logger.error("Livestream image not found")
        return "", 404
