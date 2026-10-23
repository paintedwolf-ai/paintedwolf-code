"""Read the session compositor's globals and its single output's sizes over the Wayland wire."""
from __future__ import annotations

import os
from pathlib import Path
import socket
import struct

INTERESTING = {'xdg_toplevel_drag_manager_v1', 'wl_data_device_manager', 'wl_output', 'zxdg_output_manager_v1'}
MESSAGE_LIMIT = 4096
EVENT_LIMIT = 4096


def message(object_id: int, opcode: int, payload: bytes = b'') -> bytes:
    return struct.pack('<II', object_id, (8 + len(payload)) << 16 | opcode) + payload


def string(value: str) -> bytes:
    raw = value.encode() + b'\0'
    return struct.pack('<I', len(raw)) + raw + b'\0' * (-len(raw) % 4)


def read_string(payload: bytes, offset: int) -> tuple[str, int]:
    (length,) = struct.unpack_from('<I', payload, offset)
    start, end = offset + 4, offset + 4 + length
    if length == 0 or end > len(payload) or payload[end - 1] != 0:
        raise ValueError('Wayland string argument is malformed')
    return payload[start:end - 1].decode(), end + (-length % 4)


class Wire:
    def __init__(self, connection):
        self.connection, self.buffer, self.events = connection, b'', 0

    def send(self, data: bytes):
        self.connection.sendall(data)

    def event(self) -> tuple[int, int, bytes]:
        while len(self.buffer) < 8 or len(self.buffer) < (struct.unpack_from('<I', self.buffer, 4)[0] >> 16):
            block = self.connection.recv(4096)
            if not block:
                raise ValueError('Wayland compositor closed the connection')
            self.buffer += block
        object_id, header = struct.unpack_from('<II', self.buffer)
        size = header >> 16
        if size < 8 or size > MESSAGE_LIMIT:
            raise ValueError('Wayland event size is invalid')
        payload, self.buffer = self.buffer[8:size], self.buffer[size:]
        self.events += 1
        if self.events > EVENT_LIMIT:
            raise ValueError('Wayland compositor exceeded the observation event bound')
        if object_id == 1 and header & 0xffff == 0:
            raise ValueError('Wayland compositor reported a protocol error')
        return object_id, header & 0xffff, payload

    def until_done(self, callback: int, handle):
        while True:
            object_id, opcode, payload = self.event()
            if object_id == callback and opcode == 0:
                return
            handle(object_id, opcode, payload)


def observe_on(connection) -> dict:
    wire = Wire(connection)
    wire.send(message(1, 1, struct.pack('<I', 2)) + message(1, 0, struct.pack('<I', 3)))
    globals_, outputs = {}, []

    def on_global(object_id, opcode, payload):
        if object_id == 2 and opcode == 0:
            (name,) = struct.unpack_from('<I', payload)
            interface, offset = read_string(payload, 4)
            (version,) = struct.unpack_from('<I', payload, offset)
            if interface == 'wl_output':
                outputs.append((name, version))
            if interface in INTERESTING:
                globals_[interface] = max(version, globals_.get(interface, 0))

    wire.until_done(3, on_global)
    result = {'globals': globals_, 'outputs': len(outputs), 'mode': None, 'logical_size': None}
    manager = globals_.get('zxdg_output_manager_v1')
    if len(outputs) != 1 or manager is None:
        return result
    (output_name, output_version), = outputs
    manager_name = None

    def find_manager(object_id, opcode, payload):
        nonlocal manager_name
        if object_id == 4 and opcode == 0:
            (name,) = struct.unpack_from('<I', payload)
            interface, _ = read_string(payload, 4)
            if interface == 'zxdg_output_manager_v1':
                manager_name = name

    wire.send(message(1, 1, struct.pack('<I', 4)) + message(1, 0, struct.pack('<I', 5)))
    wire.until_done(5, find_manager)
    if manager_name is None:
        return result
    bind = lambda name, interface, version, new_id: message(4, 0, struct.pack('<I', name) + string(interface)
                                                                + struct.pack('<II', version, new_id))
    wire.send(bind(output_name, 'wl_output', min(output_version, 2), 6)
              + bind(manager_name, 'zxdg_output_manager_v1', min(manager, 3), 7)
              + message(7, 1, struct.pack('<II', 8, 6))
              + message(1, 0, struct.pack('<I', 9)))

    def on_output(object_id, opcode, payload):
        if object_id == 6 and opcode == 1:
            flags, width, height, _ = struct.unpack_from('<Iiii', payload)
            if flags & 1:
                result['mode'] = [width, height]
        elif object_id == 8 and opcode == 1:
            result['logical_size'] = list(struct.unpack_from('<ii', payload))

    wire.until_done(9, on_output)
    return result


def observe(environment: dict | None = None) -> dict:
    """Connects to the session compositor named by the environment, read only."""
    environment = os.environ if environment is None else environment
    runtime, display = environment.get('XDG_RUNTIME_DIR'), environment.get('WAYLAND_DISPLAY')
    if not runtime or not display:
        raise ValueError('Wayland observation requires the session runtime directory and display')
    path = Path(display) if display.startswith('/') else Path(runtime) / display
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(5)
        connection.connect(str(path))
        return observe_on(connection)
