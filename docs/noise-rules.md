# Noise rules

## Noise rules

Rules are data, loaded from a file, tuned without a redeploy:

```yaml
rules:
  - path: /body/**/updatedAt
    ignore: true
    reason: ...
  - path: /body/total
    normalise: round:2
    reason: ...
  - path: /body/**/tags
    normalise: sort
    reason: ...
  - path: /body/**/token
    normalise: len
    reason: ...
```

`*` matches one path segment, `**` any number. Later rules override earlier ones,
so an operator can re-enable something the defaults hid. `ignore` drops a
difference; `normalise` makes the narrower claim that the values must still
agree, just not exactly (`round:N`, `sort`, `trim`, `lower`, `len`).

The ruleset is consulted **during** comparison, not after — an array's ordering
and a float's precision can only be judged where both values still exist. Arrays
are offered whole before their elements are walked, which is what makes `sort`
possible at all.

**Every suppression is counted and reported.** Suppression must never be
mistakable for agreement, which is why the report leads with "N differences
suppressed by noise rules" and why a rule that neither ignores nor normalises is
rejected at load rather than silently doing nothing.

`noise.example.yaml` is a starting point. The built-in defaults are deliberately
short (Date, Server, request ids, Set-Cookie, Age, ETag) — a long default ruleset
hides real findings on day one, and a test fails if it grows past ten rules
without justification.
