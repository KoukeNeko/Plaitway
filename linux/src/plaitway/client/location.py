"""Where the helper listens, and whether what listens there may be trusted."""

from __future__ import annotations

import os
import stat
from collections.abc import Mapping

from .errors import DaemonFailure, FailureKind

# The directory belongs to root and the socket (mode 0666: each call is
# authorized by the daemon from the peer's uid and groups) is created by the
# helper, plaitwayd.service.
PRODUCTION_SOCKET_PATH = "/run/plaitway/plaitwayd.sock"
HELPER_UNIT = "plaitwayd.service"

DEV_VARIABLE = "PLAITWAY_DEV"
SOCKET_VARIABLE = "PLAITWAY_SOCKET"


def override(environment: Mapping[str, str] | None = None) -> str | None:
    """The socket named by PLAITWAY_SOCKET, for a daemon started with `-socket`
    (`plaitwayd -fake`, the development daemon). The app does not manage the
    daemon behind such a socket, and it keeps the passwords it is given in memory.

    It counts only together with PLAITWAY_DEV=1. The app answers the credential
    requests of the daemon behind its socket from the keyring, so one that
    followed an environment variable in a normal run would hand every saved
    password to whatever process the user, or anything running as the user, put
    on that socket.
    """
    environment = os.environ if environment is None else environment
    path = environment.get(SOCKET_VARIABLE, "")
    if environment.get(DEV_VARIABLE) == "1" and path:
        return path
    return None


def socket_path(environment: Mapping[str, str] | None = None) -> str:
    """The socket the app connects to."""
    return override(environment) or PRODUCTION_SOCKET_PATH


def verify_socket_owner(path: str, trusted_uid: int = 0) -> None:
    """Raises DaemonFailure(SERVER_REFUSED) unless `path` is a socket that the
    helper's user owns, in a directory that user owns and nobody else can write to.

    Nothing is followed: a symbolic link in place of the socket or the directory
    is refused (lstat), and so is a directory that another user could put a
    different socket in. A socket that does not exist is not a refusal; the
    connection fails as an unavailable daemon.
    """
    directory = os.path.dirname(path)
    try:
        directory_info = os.lstat(directory)
        socket_info = os.lstat(path)
    except FileNotFoundError:
        return
    except OSError as error:
        raise DaemonFailure(FailureKind.SERVER_REFUSED, f"cannot examine {path}: {error.strerror}") from error

    if not stat.S_ISDIR(directory_info.st_mode):
        raise _refused(directory, "is not a directory")
    if directory_info.st_uid != trusted_uid:
        raise _refused(directory, f"is owned by user {directory_info.st_uid}")
    if directory_info.st_mode & (stat.S_IWGRP | stat.S_IWOTH):
        raise _refused(directory, "is writable by others")
    if not stat.S_ISSOCK(socket_info.st_mode):
        raise _refused(path, "is not a socket")
    if socket_info.st_uid != trusted_uid:
        raise _refused(path, f"is owned by user {socket_info.st_uid}")


def _refused(path: str, reason: str) -> DaemonFailure:
    return DaemonFailure(FailureKind.SERVER_REFUSED, f"{path} {reason}")
