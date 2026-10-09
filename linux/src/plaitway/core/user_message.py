"""What the person is told about a failure. The daemon's own text is shown only
where it explains the input (a rejected profile)."""

from __future__ import annotations

from ..client.errors import DaemonFailure, FailureKind
from ..client.profile_importer import (
    MissingFile,
    NotCredentials,
    NotKeyMaterial,
    NotText,
    OutsideProfileDirectory,
    ProfileImportError,
    TooLarge,
    Unreadable,
)
from ..l10n.strings import Strings
from .ports import CredentialStoreError, HelperServiceError


def user_message(error: BaseException, strings: Strings) -> str:
    if isinstance(error, ProfileImportError):
        return import_failure_message(error, strings)
    if isinstance(error, CredentialStoreError):
        return strings.secret_service_unavailable if error.unavailable else str(error)
    if isinstance(error, HelperServiceError):
        return str(error)
    failure = DaemonFailure.from_error(error)
    match failure.kind:
        case FailureKind.PERMISSION_DENIED:
            return strings.administrator_required
        case FailureKind.UNAVAILABLE:
            return strings.helper_unavailable
        case FailureKind.NOT_FOUND:
            return strings.profile_not_found
        case FailureKind.SERVER_REFUSED:
            return strings.helper_not_trusted
        case _:
            return failure.message


def import_failure_message(error: ProfileImportError, strings: Strings) -> str:
    match error:
        case Unreadable():
            return strings.cannot_read(error.path, error.reason)
        case NotText():
            return strings.not_a_text_file(error.path)
        case TooLarge():
            return strings.file_too_large(error.path)
        case MissingFile():
            return strings.file_not_found(error.directive, error.path)
        case NotKeyMaterial():
            return strings.file_holds_no_key(error.directive, error.path)
        case OutsideProfileDirectory():
            return strings.file_outside_folder(error.directive, error.path)
        case NotCredentials():
            return strings.credentials_file_invalid(error.path)
        case _:
            return str(error)
