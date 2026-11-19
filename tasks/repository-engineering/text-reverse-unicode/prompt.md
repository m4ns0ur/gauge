# Make reverse handle Unicode correctly

`text.Reverse` currently reverses bytes, so text containing multibyte UTF-8
characters is mangled. Change it to reverse characters (Unicode code points)
instead, while preserving its behavior for plain ASCII text.

Requirements:

- `Reverse` must return the input characters in reverse order.
- Multibyte characters (accented letters, emoji, CJK) must stay intact.
- ASCII behavior must not change.
- Do not change the visible tests or add dependencies.

The module must keep building and its existing tests must pass.
