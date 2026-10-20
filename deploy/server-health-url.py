#!/usr/bin/env python3
"""Print the local health URL for a saved Coconet server configuration."""
import ipaddress
import json
import re
import sys


def health_url(config):
    listen = config["listen"]
    host, separator, port = listen.rpartition(":")
    if not separator or not port.isascii() or not port.isdecimal() or not 0 < int(port) < 65536:
        raise ValueError("listen must include a fixed TCP port")
    if host.startswith("[") and host.endswith("]"):
        host = host[1:-1]
    try:
        address = ipaddress.ip_address(host)
    except ValueError:
        if host and not re.fullmatch(r"[a-zA-Z0-9.-]+", host):
            raise ValueError("invalid listen hostname")
        host = host or "127.0.0.1"
    else:
        if address.is_unspecified:
            host = "::1" if address.version == 6 else "127.0.0.1"
        if address.version == 6:
            host = "[" + host + "]"
    return "http://" + host + ":" + port + "/healthz"


if __name__ == "__main__":
    try:
        with open(sys.argv[1], encoding="utf-8") as source:
            print(health_url(json.load(source)))
    except (IndexError, KeyError, TypeError, AttributeError, ValueError, OSError) as error:
        sys.exit("cannot determine server health URL: " + str(error))
