"""Python Language Server used to implement autocompletion in the
native environment on the robot.

For more information see:
https://code.visualstudio.com/api/language-extensions/language-server-extension-guide

JSON-RPC docs: https://json-rpc.readthedocs.io/en/latest/quickstart.html
pyls_jsonrpc docs: https://github.com/palantir/python-language-server

Note: Autocomplete for robot library requires robot lib to be in PYTHONPATH.
Crashes with the bellow upon the user closing the tab. This is expected behaviour
but it would be nice to make the logs nicer:
INFO:root:Exiting pyls with the JSON object must be str, bytes or bytearray, not
'NoneType'
"""
import logging
import json
import subprocess
import threading
import flask

from pyls_jsonrpc import streams
from geventwebsocket import WebSocketError

blueprint = flask.Blueprint("pyls", __name__)


@blueprint.route("/pyls")
def pyls_socket(socket):
    """Handle messages between the client and a `pyls` process"""
    ls_process = subprocess.Popen(
        ['pyls', '-v'],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE
    )

    server_interface = streams.JsonRpcStreamWriter(ls_process.stdin)

    def process_message(message):
        serialized_json = json.dumps(message)
        socket.send(serialized_json)
        logging.debug(serialized_json)

    def server_data_to_client():
        reader = streams.JsonRpcStreamReader(ls_process.stdout)
        reader.listen(process_message)

    thread = threading.Thread(target=server_data_to_client)
    thread.daemon = True
    thread.start()

    try:
        while not socket.closed:
            message = json.loads(socket.receive())
            logging.debug(message)
            if not message is None:
                server_interface.write(message)
    except (TypeError, WebSocketError) as err:
        # When the client exits these errors can be raised
        logging.info("Exiting pyls because {}".format(err))
    finally:
        ls_process.kill()
