# Design documents

Substantial changes get a document here before they get code.

A design doc that only describes what was built is a summary, and the code is
already a better summary. The value is in the part that does not survive into the
code: **the alternatives that were considered and rejected, and why**. That is the
part a reader cannot reconstruct, and it is the part that stops the same rejected
idea being re-proposed every six months.

## When to write one

Write a design doc when a change is hard to reverse: a new invariant, a new
dependency, a change to the data model or the public API, or anything that
constrains what can be built later. Skip it for bug fixes, refactors that preserve
behaviour, and anything a reviewer can hold in their head.

If you cannot say what the change makes *harder*, the doc is not finished.

## Format

Numbered, immutable once accepted. A superseded document is marked as such and
kept — the reasoning that turned out to be wrong is often more instructive than
the reasoning that turned out to be right.

```
docs/design/0001-deterministic-crawl.md
```

Sections, in order:

1. **Status** — draft, accepted, superseded by NNNN, or rejected.
2. **Problem** — what is broken or absent, with evidence. Not "it would be nice
   if"; what specifically fails today.
3. **Constraints** — what the solution must not break.
4. **Proposal** — the design, in enough detail to argue with.
5. **Alternatives considered** — each with why it was rejected. At least two, or
   the design was not chosen so much as stumbled into.
6. **Consequences** — what this makes easier, what it makes harder, and what it
   forecloses.
7. **Open questions** — what is still unresolved. An empty section here is
   usually a sign of insufficient thought, not sufficient thought.

## Index

| # | Title | Status |
|---|---|---|
| [0001](0001-deterministic-crawl.md) | Deterministic crawl and replay | Draft |
