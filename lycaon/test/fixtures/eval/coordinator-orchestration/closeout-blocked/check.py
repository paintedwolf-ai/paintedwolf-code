import socket
from report import select

with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
    probe.sendto(b"ok", ("127.0.0.1", 9))
rows = [{"name": str(i), "score": i} for i in range(4)]
assert select(rows) == [rows[3], rows[2], rows[1]]
print("ok")
