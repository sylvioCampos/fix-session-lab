# Data dictionary

`FIX44-fixlab.xml` is the stock **FIX 4.4** dictionary shipped with
[quickfixgo/quickfix](https://github.com/quickfixgo/quickfix) (`spec/FIX44.xml`,
v0.9.10), with three changes to the `Logon` message and nothing else.

## The delta

Added to `<fields>`:

```xml
<field number='35002' name='CODType' type='INT'>
 <value enum='0' description='DISABLED' />
 <value enum='1' description='CANCEL_ON_DISCONNECT' />
 <value enum='2' description='CANCEL_ON_LOGOUT' />
 <value enum='3' description='CANCEL_ON_DISCONNECT_OR_LOGOUT' />
</field>
<field number='35003' name='CODTimeoutWindow' type='INT' />
```

Added to `<message name='Logon' msgtype='A'>`:

```xml
<field name='Text' required='N' />
<field name='CODType' required='N' />
<field name='CODTimeoutWindow' required='N' />
```

## Text (58) on Logon

`Text` is the one addition that is not a custom tag, and it is the one most
likely to bite you first. Stock FIX 4.4 does not list `Text` on `Logon`. B3
EntryPoint does, and expects clients to send an application identifier there.

Without the line above, an engine validating against the stock dictionary
answers an inbound Logon carrying `58=` with *"Tag not defined for this message
type"* and drops the connection — before `OnLogon` ever fires, so the
application never sees it. The symptom is a session that connects, immediately
disconnects, and reconnects forever, with nothing in the application log to
explain it.

The mirror image of this bites too. Marking `Text` `required='Y'` because your
venue mandates it on outbound Logons will make the engine reject the venue's own
Logon *response*, which typically omits `Text`. A dictionary's `required` flag
is not directional: it applies to both sides of the conversation. Enforce what
you must send in your application code, and keep the dictionary permissive.

## Why extend the dictionary at all

Tags above 5000 are user-defined. With `ValidateUserDefinedFields=N` an engine
will let them through undeclared — which is what most integrations do at first,
and why a typo in a custom tag number can go unnoticed for months. Declaring
them buys you validation and readable log output, at the cost of maintaining a
copy of the dictionary. This lab declares them so that a wrong `CODType` value
fails loudly in a drill rather than silently on the wire.

A real B3 EntryPoint dictionary carries roughly fifty custom tags in the 35xxx
range. This lab ships two, because two is what the drills exercise.

## On B3

The tag numbers and enum values above are stated as bare facts so the drills can
run. B3's specifications are **not** reproduced here — no field tables, no enum
catalogues, no prose. Obtain the authoritative documents (`EntryPoint Messaging
Guidelines`, `EntryPoint Message Specs`) directly from B3.

See [`../NOTICE.md`](../NOTICE.md).
