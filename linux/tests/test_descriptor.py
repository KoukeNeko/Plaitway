"""The committed descriptor is the client's whole knowledge of the API: it has to
agree with proto/plaitway/v1/plaitway.proto, and the client has a method for each call."""

import re
import tempfile
import unittest
from pathlib import Path

from google.protobuf import descriptor_pb2

from plaitway.client import proto
from plaitway.client.daemon_client import DaemonClient

LINUX_DIR = Path(__file__).resolve().parents[1]
PROTO_DIR = LINUX_DIR.parent / "proto"


def summary(files: descriptor_pb2.FileDescriptorSet) -> dict:
    """What the API is, without what a protoc version adds to a descriptor of it."""
    result: dict = {}
    for file in files.file:
        for message in file.message_type:
            result[f"{file.package}.{message.name}"] = [
                (field.name, field.number, field.type, field.label, field.type_name, field.oneof_index if field.HasField("oneof_index") else None,
                 field.proto3_optional)
                for field in message.field
            ]
        for enum in file.enum_type:
            result[f"{file.package}.{enum.name}"] = [(value.name, value.number) for value in enum.value]
        for service in file.service:
            result[f"{file.package}.{service.name}"] = [
                (method.name, method.input_type, method.output_type, method.server_streaming) for method in service.method
            ]
    return result


class DescriptorTests(unittest.TestCase):
    def committed(self) -> descriptor_pb2.FileDescriptorSet:
        return descriptor_pb2.FileDescriptorSet.FromString(proto.DESCRIPTOR_PATH.read_bytes())

    def test_agrees_with_the_proto_file(self):
        try:
            import grpc_tools
            from grpc_tools import protoc
        except ImportError:
            self.skipTest("grpcio-tools is not installed: tools/gen_descriptor.py --check is the check")
        with tempfile.TemporaryDirectory() as scratch:
            fresh = Path(scratch) / "fresh.binpb"
            status = protoc.main([
                "protoc", f"-I{PROTO_DIR}", f"-I{Path(grpc_tools.__file__).parent / '_proto'}",
                f"--descriptor_set_out={fresh}", "--include_imports", "plaitway/v1/plaitway.proto",
            ])
            self.assertEqual(status, 0)
            expected = descriptor_pb2.FileDescriptorSet.FromString(fresh.read_bytes())
        self.assertEqual(summary(self.committed()), summary(expected), "tools/gen_descriptor.py has to be run again")

    def test_has_a_method_in_the_client_for_every_call(self):
        calls = proto.methods()
        self.assertGreaterEqual(len(calls), 14)
        for name, call in calls.items():
            snake = re.sub(r"(?<!^)(?=[A-Z])", "_", name).lower()
            if call.streams:
                snake = snake.replace("watch_", "watch_")
            self.assertTrue(hasattr(DaemonClient, snake), f"{name}: the client has no {snake}")

    def test_builds_the_messages_the_client_needs(self):
        from plaitway.client import types

        profile = types.Profile(id="p", name="n", state=types.ProfileState.CONNECTED)
        self.assertEqual(types.Profile.FromString(profile.SerializeToString()), profile)
        self.assertEqual(types.ProfileState(99), types.ProfileState.UNSPECIFIED, "an unknown number reads as unspecified")
        self.assertEqual(types.ProfileState.AWAITING_CREDENTIALS, 7)
        self.assertEqual(types.TunnelMode.SPLIT, 3)

    def test_the_wire_names_of_the_service_are_the_protos(self):
        calls = proto.methods()
        self.assertEqual(calls["WatchProfiles"].path, "/plaitway.v1.DaemonService/WatchProfiles")
        self.assertTrue(calls["WatchProfiles"].streams and calls["WatchLogs"].streams)
        self.assertFalse(calls["GetDaemonInfo"].streams)


if __name__ == "__main__":
    unittest.main()
