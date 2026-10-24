"""Bounded pointer encoding for steps selected by the closed input operation registry."""
from __future__ import annotations

from dataclasses import dataclass

ABSOLUTE_MAX = 0x7fff


def display_size(value):
    if (type(value) is not tuple or len(value) != 2
            or any(type(item) is not int for item in value)
            or not 320 <= value[0] <= 16384 or not 240 <= value[1] <= 16384):
        raise ValueError('pointer display has no bounded framebuffer dimensions')
    return value


@dataclass(frozen=True)
class PointerStep:
    event: tuple
    display: tuple[int, int]

    def __post_init__(self):
        display_size(self.display)
        event = self.event
        if type(event) is not tuple or not event or type(event[0]) is not str:
            raise ValueError('pointer step has no fixed event tuple')
        if event[0] == '@move' and len(event) == 3:
            if (any(type(item) is not int for item in event[1:])
                    or not 0 <= event[1] < self.display[0] or not 0 <= event[2] < self.display[1]):
                raise ValueError('pointer move is outside its framebuffer')
        elif (event[0] != '@button' or len(event) != 2 or type(event[1]) is not str
              or event[1] not in {'down', 'up'}):
            raise ValueError('pointer step is not a move or left-button transition')

    def qmp_events(self):
        self.__post_init__()
        if self.event[0] == '@move':
            return [{'type': 'abs', 'data': {'axis': axis, 'value': value * ABSOLUTE_MAX // (size - 1)}}
                    for axis, value, size in zip(('x', 'y'), self.event[1:], self.display)]
        return [{'type': 'btn', 'data': {'down': self.event[1] == 'down', 'button': 'left'}}]


def sequence_display(sequence):
    """Validate the whole selected sequence before its first possible input effect."""
    selected, down, moved = None, False, False
    for step in sequence:
        if not isinstance(step, PointerStep):
            if (type(step) is not tuple or not step or any(type(key) is not str for key in step)
                    or step[0].startswith('@')):
                raise ValueError('raw pointer tuples are not selected typed steps')
            continue
        step.__post_init__()
        if selected is not None and selected != step.display:
            raise ValueError('pointer sequence changes its framebuffer dimensions')
        selected = step.display
        if step.event[0] == '@move':
            moved = True
        else:
            press = step.event[1] == 'down'
            if not moved or press == down:
                raise ValueError('pointer sequence has an unowned button transition')
            down = press
    if down:
        raise ValueError('pointer sequence omits its owned button release')
    return selected


def framebuffer_size(raw):
    """Decode only QEMU's small binary PPM header, never retain its pixel payload."""
    if type(raw) is not bytes or len(raw) > 128:
        raise ValueError('QEMU framebuffer header exceeds its fixed bound')
    fields = raw.split(None, 4)
    if (len(fields) < 4 or fields[0] != b'P6' or fields[3] != b'255'
            or any(not field.isdigit() or len(field) > 5 for field in fields[1:3])):
        raise ValueError('QEMU framebuffer has no declared binary PPM header')
    return display_size(tuple(int(field) for field in fields[1:3]))
