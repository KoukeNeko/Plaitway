"""Why a call to the daemon failed, in the terms a UI words differently."""

from __future__ import annotations

import enum

import grpc


class FailureKind(enum.Enum):
    # The caller is not an administrator, or not the console user.
    PERMISSION_DENIED = "permission denied"
    # The daemon is not running or not reachable.
    UNAVAILABLE = "unavailable"
    NOT_FOUND = "not found"
    # The daemon refused the input; the message is its reason.
    REJECTED = "rejected"
    # This app refused the daemon: whatever listens on the socket is not the
    # helper, so nothing was sent to it.
    SERVER_REFUSED = "server refused"
    OTHER = "other"


class DaemonFailure(Exception):
    """A failed call. `message` is the daemon's English text, meant to be shown
    only where the cause is the input (a rejected profile) or the refusal of
    the server."""

    def __init__(self, kind: FailureKind, message: str = "") -> None:
        super().__init__(kind.value, message)
        self.kind = kind
        self.message = message

    def __eq__(self, other: object) -> bool:
        return isinstance(other, DaemonFailure) and (self.kind, self.message) == (other.kind, other.message)

    def __hash__(self) -> int:
        return hash((self.kind, self.message))

    def __str__(self) -> str:
        return f"{self.kind.value}: {self.message}" if self.message else self.kind.value

    @classmethod
    def from_error(cls, error: BaseException) -> DaemonFailure:
        if isinstance(error, DaemonFailure):
            return error
        if isinstance(error, grpc.RpcError) and hasattr(error, "code"):
            return cls._from_status(error.code(), error.details() or "")
        return cls(FailureKind.OTHER, str(error))

    @classmethod
    def _from_status(cls, code: grpc.StatusCode, details: str) -> DaemonFailure:
        match code:
            case grpc.StatusCode.PERMISSION_DENIED:
                return cls(FailureKind.PERMISSION_DENIED, details)
            case grpc.StatusCode.UNAVAILABLE:
                return cls(FailureKind.UNAVAILABLE, details)
            case grpc.StatusCode.NOT_FOUND:
                return cls(FailureKind.NOT_FOUND, details)
            case grpc.StatusCode.INVALID_ARGUMENT | grpc.StatusCode.FAILED_PRECONDITION:
                return cls(FailureKind.REJECTED, details)
            case _:
                return cls(FailureKind.OTHER, details)
