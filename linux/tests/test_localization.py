"""The string catalogs are edited by hand and the accessors are generated from them,
so these tests keep the two in step with the sources and with the owner's rules for
Traditional Chinese and for UI text."""

import ast
import importlib.util
import json
import re
import sys
import unittest
from pathlib import Path

LINUX_DIR = Path(__file__).resolve().parents[1]
SOURCES = LINUX_DIR / "src" / "plaitway"
GENERATED = SOURCES / "l10n" / "strings.py"


def load_tool():
    spec = importlib.util.spec_from_file_location("localize_tool", LINUX_DIR / "tools" / "localize.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module  # a dataclass looks its module up
    spec.loader.exec_module(module)
    return module


TOOL = load_tool()


def python_sources():
    return sorted(path for path in SOURCES.rglob("*.py") if path != GENERATED)


STRINGS_NAMES = {"strings", "s"}
STRINGS_ATTRIBUTES = {"strings", "_strings"}


def accessors_used() -> dict[str, str]:
    """Every accessor of Strings the sources use, with the file that does it."""
    used: dict[str, str] = {}
    for path in python_sources():
        for node in ast.walk(ast.parse(path.read_text(encoding="utf-8"))):
            if not isinstance(node, ast.Attribute):
                continue
            owner = node.value
            if (isinstance(owner, ast.Name) and owner.id in STRINGS_NAMES) or (
                isinstance(owner, ast.Attribute) and owner.attr in STRINGS_ATTRIBUTES
            ):
                used.setdefault(node.attr, path.name)
    return used


def accessors_of_strings() -> set[str]:
    from plaitway.l10n.strings import Strings

    return {name for name in dir(Strings) if not name.startswith("_") and name != "language"}


class GeneratedModuleTests(unittest.TestCase):
    def test_the_committed_module_is_what_the_generator_makes(self):
        self.assertEqual(
            GENERATED.read_text(encoding="utf-8"),
            TOOL.render(TOOL.build_entries()),
            "tools/localize.py has to be run again",
        )

    def test_every_accessor_the_sources_use_exists(self):
        used = accessors_used()
        self.assertGreater(len(used), 100, f"the scan found too little: {len(used)}")
        missing = {name: path for name, path in used.items() if name not in accessors_of_strings()}
        self.assertEqual(missing, {}, "not in the catalogs")

    def test_the_catalogs_hold_no_string_nobody_uses(self):
        unused = sorted(accessors_of_strings() - set(accessors_used()))
        self.assertEqual(unused, [], "unused: delete them from the catalog, or list a macOS string in macosOnly")

    def test_the_strings_of_macos_that_linux_does_not_use_are_listed(self):
        manifest = TOOL.load_manifest()
        macos = TOOL.load_catalog(TOOL.MACOS_CATALOG)
        used_keys = {entry.key for entry in TOOL.build_entries()}
        # Every string of the macOS catalog is either an accessor or says it is not used.
        self.assertEqual(set(macos) - used_keys, set(manifest["macosOnly"]))

    def test_a_linux_string_does_not_repeat_a_macos_one(self):
        self.assertEqual(set(TOOL.load_catalog(TOOL.LINUX_CATALOG)) & set(TOOL.load_catalog(TOOL.MACOS_CATALOG)), set())


class NoTextBypassesTheCatalogTests(unittest.TestCase):
    """UI code takes its words from the accessors: a literal that a control shows is a
    string no translation reaches."""

    PRODUCT_NAMES = {"Plaitway", "OpenVPN", "WireGuard", "DNS"}
    KEYWORDS = {"label", "title", "subtitle", "tooltip_text", "placeholder_text", "description", "heading", "body", "text"}
    SETTERS = {"set_label", "set_title", "set_subtitle", "set_tooltip_text", "set_placeholder_text", "set_description",
               "set_heading", "set_body", "set_text"}
    # (callee, position) of helpers that take a text as a positional argument.
    HELPERS = {"label": 0, "text_button": 0, "icon_button": 1, "status_page": 1, "add_response": 1, "append": None}

    def shown_literals(self, tree: ast.AST):
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            candidates = [keyword.value for keyword in node.keywords if keyword.arg in self.KEYWORDS]
            name = node.func.attr if isinstance(node.func, ast.Attribute) else getattr(node.func, "id", "")
            if name in self.SETTERS and node.args:
                candidates.append(node.args[0])
            position = self.HELPERS.get(name)
            if position is not None and len(node.args) > position:
                candidates.append(node.args[position])
            for value in candidates:
                if isinstance(value, ast.Constant) and isinstance(value.value, str):
                    yield node.lineno, value.value

    def test_no_control_is_given_a_literal_text(self):
        offenders = []
        for path in python_sources():
            if "app" not in path.relative_to(SOURCES).parts[:1]:
                continue
            for line, text in self.shown_literals(ast.parse(path.read_text(encoding="utf-8"))):
                if re.search(r"[A-Za-z]", text) and text not in self.PRODUCT_NAMES:
                    offenders.append(f"{path.name}:{line}: {text!r}")
        self.assertEqual(offenders, [])


class CatalogRuleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.catalog = {**TOOL.load_catalog(TOOL.MACOS_CATALOG), **TOOL.load_catalog(TOOL.LINUX_CATALOG)}
        cls.used = {entry.key: entry for entry in TOOL.build_entries()}

    def test_every_string_is_translated_into_traditional_chinese(self):
        for catalog in (TOOL.MACOS_CATALOG, TOOL.LINUX_CATALOG):
            data = json.loads(catalog.read_text(encoding="utf-8"))
            self.assertEqual(data["sourceLanguage"], "en")
            for key, entry in data["strings"].items():
                unit = entry["localizations"]["zh-Hant"]["stringUnit"]
                self.assertEqual(unit["state"], "translated", key)
                self.assertTrue(unit["value"], key)
                self.assertEqual(sorted(TOOL.specifiers(key)), sorted(TOOL.specifiers(unit["value"])), f"{key}: the placeholders differ")

    def test_both_languages_answer_for_every_accessor(self):
        from plaitway.l10n.strings import LANGUAGES, Strings

        for language in LANGUAGES:
            strings = Strings(language)
            for entry in self.used.values():
                accessor = getattr(strings, entry.name)
                if entry.params:
                    value = accessor(*("x" if kind == "str" else 1 for _, kind in entry.params))
                else:
                    value = accessor
                self.assertTrue(value, (language, entry.name))

    def test_the_translations_use_taiwan_terms(self):
        # Mainland terms and their Taiwan replacements: 匹配 -> 配對/符合, 當前 -> 目前, 組件 -> 元件, 緩存 -> 快取, ...
        forbidden = ["匹配", "當前", "組件", "緩存", "軟件", "默認", "設置", "用戶", "網絡", "信息", "服務器", "登錄", "程序", "文件", "數據", "鏈接", "賬", "視頻", "支持"]
        for entry in self.used.values():
            value = entry.translations["zh-Hant"]
            for term in forbidden:
                self.assertNotIn(term, value, f"{entry.key} -> {value} uses {term}")

    def test_the_text_carries_no_personality(self):
        # Routine states, errors and dialogs: no exclamation marks, no "we" or "your", no replies as buttons.
        banned_english = [" we ", "we ", "your ", "!", "please", "oops", "sorry", "successfully"]
        banned_chinese = ["我們", "你的", "您的", "！", "請稍候", "好的", "是的", "抱歉", "成功"]
        for entry in self.used.values():
            english = entry.key.lower()
            for word in banned_english:
                self.assertNotIn(word, english, entry.key)
            for word in banned_chinese:
                self.assertNotIn(word, entry.translations["zh-Hant"], entry.key)

    def test_a_figure_is_not_explained_in_words(self):
        # 「約 140 kcal」 and 「128 / 2,400 mg 上限」: the figure and its layout already say it.
        for entry in self.used.values():
            self.assertNotIn("約", entry.translations["zh-Hant"], entry.key)
            self.assertNotIn("上限", entry.translations["zh-Hant"], entry.key)

    def test_buttons_name_the_action_and_do_not_reply(self):
        for entry in self.used.values():
            self.assertNotIn(entry.key, {"OK", "Yes", "No", "Okay", "Sure"}, entry.key)

    def test_the_same_action_has_the_same_label_everywhere(self):
        used = set(accessors_used())
        # One wording for each action wherever it appears.
        for name in ["import_profile", "delete_profile", "connect", "disconnect", "retry", "restart_helper", "cancel", "remove"]:
            self.assertIn(name, used, f"{name} is not used")
        ellipsis = sorted(entry.key for entry in self.used.values() if entry.key.endswith("…"))
        self.assertEqual(ellipsis, ["Delete Profile…", "Import Profile…", "Settings…"])
        self.assertEqual([k for k in self.used if k.lower().startswith("import") and "…" in k], ["Import Profile…"])
        self.assertEqual([k for k in self.used if k.startswith("Delete") and k.endswith("…")], ["Delete Profile…"])

    def test_a_label_is_a_noun_or_a_short_state_not_a_question_or_coaching(self):
        # Questions are for dialogs that ask (a title that names the action); never for a label or a status.
        questions = sorted(entry.key for entry in self.used.values() if entry.key.endswith("?"))
        for key in questions:
            entry = self.used[key]
            self.assertTrue(
                entry.name.endswith("_title") or entry.name.startswith("quit_with"), f"{key} is a question that is not a dialog's title"
            )
        for entry in self.used.values():
            self.assertNotIn("anytime", entry.key.lower())
            self.assertNotIn("you can", entry.key.lower())

    def test_the_language_follows_the_desktop(self):
        from plaitway.l10n.strings import Strings

        self.assertEqual(Strings("zh-Hant").connected, "已連線")
        self.assertEqual(Strings("zh-Hant").shadowed_by("Office"), "被 Office 遮蔽")
        self.assertEqual(Strings("en").connected_count(2), "Connected: 2")
        # A language the app does not have reads as English.
        self.assertEqual(Strings("fr").connected, "Connected")
        self.assertEqual(Strings("fr").language, "en")


if __name__ == "__main__":
    unittest.main()
