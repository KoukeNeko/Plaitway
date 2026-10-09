"""The wire types of proto/plaitway/v1/plaitway.proto, built at run time.

Generated *_pb2.py modules only load with the protobuf version they were made
by, and this app runs on whatever python3-protobuf the distribution has. So the
repository holds a serialized FileDescriptorSet (descriptor.binpb, written by
tools/gen_descriptor.py) and the classes are made from it here, in a private
descriptor pool.
"""

from __future__ import annotations

import enum
import re
from dataclasses import dataclass
from pathlib import Path

from google.protobuf import descriptor_pb2, descriptor_pool, message_factory

PACKAGE = "plaitway.v1"
SERVICE = "DaemonService"
DESCRIPTOR_PATH = Path(__file__).with_name("descriptor.binpb")


def _load_pool() -> descriptor_pool.DescriptorPool:
    files = descriptor_pb2.FileDescriptorSet.FromString(DESCRIPTOR_PATH.read_bytes())
    pool = descriptor_pool.DescriptorPool()
    # --include_imports lists a file after the files it imports.
    for file in files.file:
        pool.AddSerializedFile(file.SerializeToString())
    return pool


POOL = _load_pool()

if hasattr(message_factory, "GetMessageClass"):
    def _class_for(descriptor):
        return message_factory.GetMessageClass(descriptor)
else:  # protobuf before 4.22
    _FACTORY = message_factory.MessageFactory(POOL)

    def _class_for(descriptor):
        return _FACTORY.GetPrototype(descriptor)


def message(name: str):
    """The message class of `plaitway.v1.<name>`."""
    return _class_for(POOL.FindMessageTypeByName(f"{PACKAGE}.{name}"))


class OpenEnum(enum.IntEnum):
    """A protobuf enum: a number this app does not know reads as UNSPECIFIED (0)."""

    @classmethod
    def _missing_(cls, value):
        return cls(0)


def enum_type(name: str) -> type[OpenEnum]:
    """An IntEnum of `plaitway.v1.<name>` with the enum's prefix removed from its members."""
    prefix = re.sub(r"(?<!^)(?=[A-Z])", "_", name).upper() + "_"
    descriptor = POOL.FindEnumTypeByName(f"{PACKAGE}.{name}")
    members = {value.name.removeprefix(prefix): value.number for value in descriptor.values}
    return OpenEnum(name, members)


@dataclass(frozen=True)
class Method:
    """An RPC of the service, with what its call needs."""

    path: str
    request: type
    response: type
    streams: bool


def methods() -> dict[str, Method]:
    service = POOL.FindServiceByName(f"{PACKAGE}.{SERVICE}")
    return {
        method.name: Method(
            path=f"/{PACKAGE}.{SERVICE}/{method.name}",
            request=_class_for(method.input_type),
            response=_class_for(method.output_type),
            streams=method.server_streaming,
        )
        for method in service.methods
    }
