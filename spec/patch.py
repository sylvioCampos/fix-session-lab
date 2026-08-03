#!/usr/bin/env python3
"""Regenerate spec/FIX44-fixlab.xml from quickfixgo's stock FIX 4.4 dictionary.

Run from the repository root:

    python3 spec/patch.py

The point of keeping this as a script rather than hand-editing the committed XML
is that the delta stays reviewable. Every change the lab makes to the dictionary
is visible here in about thirty lines, instead of buried in a 330 KB file.

See spec/README.md for what each change is for.
"""

import pathlib
import sys

SRC = pathlib.Path.home() / "go/pkg/mod/github.com/quickfixgo/quickfix@v0.9.10/spec/FIX44.xml"
DST = pathlib.Path("spec/FIX44-fixlab.xml")

# 1. Venue-specific custom tags, in the user-defined range.
CUSTOM_FIELDS = """  <field number='35002' name='CODType' type='INT'>
   <value enum='0' description='DISABLED' />
   <value enum='1' description='CANCEL_ON_DISCONNECT' />
   <value enum='2' description='CANCEL_ON_LOGOUT' />
   <value enum='3' description='CANCEL_ON_DISCONNECT_OR_LOGOUT' />
  </field>
  <field number='35003' name='CODTimeoutWindow' type='INT' />
"""

# 2. Text and the COD pair on Logon.
LOGON_TAIL = (
    "   <field name='Username' required='N' />\n"
    "   <field name='Password' required='N' />\n"
    "  </message>\n"
)
LOGON_PATCHED = (
    "   <field name='Username' required='N' />\n"
    "   <field name='Password' required='N' />\n"
    "   <field name='Text' required='N' />\n"
    "   <field name='CODType' required='N' />\n"
    "   <field name='CODTimeoutWindow' required='N' />\n"
    "  </message>\n"
)

# 3. A venue-specific value on a *standard* tag.
#
# Anchored on the field declaration rather than on its last <value>, because
# "enum='99' description='OTHER'" appears in twenty-two different fields.
# Ordering of <value> elements is not significant, so inserting at the top is
# as good as appending.
RESTATEMENT_ANCHOR = "  <field number='378' name='ExecRestatementReason' type='INT'>\n"
RESTATEMENT_PATCHED = (
    "  <field number='378' name='ExecRestatementReason' type='INT'>\n"
    "   <value enum='100' description='CANCEL_ON_DISCONNECT' />\n"
)


def main() -> int:
    if not SRC.exists():
        print(f"source dictionary not found: {SRC}", file=sys.stderr)
        print("run `go mod download` first", file=sys.stderr)
        return 1

    xml = SRC.read_text()

    def replace_once(text: str, old: str, new: str, what: str) -> str:
        if text.count(old) != 1:
            raise SystemExit(f"expected exactly one {what} anchor, found {text.count(old)}")
        return text.replace(old, new, 1)

    xml = replace_once(xml, " <fields>\n", " <fields>\n" + CUSTOM_FIELDS, "fields block")
    xml = replace_once(xml, LOGON_TAIL, LOGON_PATCHED, "Logon message")
    xml = replace_once(xml, RESTATEMENT_ANCHOR, RESTATEMENT_PATCHED, "ExecRestatementReason")

    DST.write_text(xml)
    print(f"wrote {DST} ({len(xml)} bytes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
