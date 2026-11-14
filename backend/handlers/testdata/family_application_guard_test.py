import io
import json
import unittest
import os
import tempfile
from pathlib import Path
from types import SimpleNamespace
import setup_family_application as setup
from unittest.mock import patch

from family_application_browser import Fixture


class Reply(io.BytesIO):
    status = 200

    def __init__(self, value):
        super().__init__(json.dumps(value).encode())


class ApplicationGuards(unittest.TestCase):
    def credentials(self):
        return {"fixture": "mediastorm-synthetic-family-v1", "username": "synthetic-family-test",
                "password": "synthetic-input", "profileId": "alpha", "secondProfileId": "other", "pin": "1357"}

    def test_non_loopback_and_credential_origins_never_contact_a_server(self):
        for base in ("http://192.0.2.1:19000", "http://example.invalid", "http://user@127.0.0.1:19000"):
            with self.subTest(base=base), patch("urllib.request.urlopen") as request:
                with self.assertRaises(ValueError):
                    Fixture(base, self.credentials())
                request.assert_not_called()

    def test_missing_marker_or_real_account_never_contacts_a_server(self):
        for field, value in (("fixture", "other"), ("username", "ordinary-account")):
            credentials = self.credentials()
            credentials[field] = value
            with self.subTest(field=field), patch("urllib.request.urlopen") as request:
                with self.assertRaises(ValueError):
                    Fixture("http://127.0.0.1:19000", credentials)
                request.assert_not_called()

    def test_non_demo_server_stops_before_profile_mutation(self):
        with patch("urllib.request.urlopen", side_effect=[Reply({"token": "synthetic-session"}), Reply({"demoMode": False})]) as request:
            with self.assertRaises(ValueError):
                Fixture("http://127.0.0.1:19000", self.credentials())
            self.assertEqual(["/api/auth/login", "/api/settings"], [call.args[0].selector for call in request.call_args_list])

    def test_foreign_or_renamed_profiles_stop_before_pin_or_progress_changes(self):
        responses = [{"token": "synthetic-session"}, {"demoMode": True}, [{"id": "foreign", "name": "Synthetic Alpha"}]]
        with patch("urllib.request.urlopen", side_effect=[Reply(v) for v in responses]) as request:
            with self.assertRaises(ValueError):
                Fixture("http://127.0.0.1:19000", self.credentials())
            self.assertEqual(["/api/auth/login", "/api/settings", "/api/users"], [call.args[0].selector for call in request.call_args_list])


    def test_setup_refuses_a_non_demo_server_before_account_creation(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory) / "bootstrap.json"
            private.write_text(json.dumps({"username": "synthetic-bootstrap", "password": "synthetic-input"}))
            os.chmod(private, 0o600)
            output = Path(directory) / "fixture.json"
            args = SimpleNamespace(base_url="http://127.0.0.1:19000", bootstrap_credentials_file=str(private), output=str(output))
            with patch("urllib.request.urlopen", side_effect=[Reply({"token": "synthetic-session"}), Reply({"demoMode": False})]) as request:
                with self.assertRaises(ValueError):
                    setup.main(args)
                self.assertEqual(["/api/auth/login", "/api/settings"], [call.args[0].selector for call in request.call_args_list])
                self.assertFalse(output.exists())

    def test_setup_never_replaces_an_existing_fixture(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "fixture.json"
            output.write_text("preserved")
            args = SimpleNamespace(base_url="http://127.0.0.1:19000", bootstrap_credentials_file="unused", output=str(output))
            with patch("urllib.request.urlopen") as request:
                with self.assertRaises(ValueError):
                    setup.main(args)
                request.assert_not_called()
                self.assertEqual("preserved", output.read_text())


if __name__ == "__main__":
    unittest.main()
