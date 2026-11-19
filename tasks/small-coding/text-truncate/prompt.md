# Implement rune-safe truncation

`text.Truncate` is not implemented. Implement it so that it returns a string
shortened to at most `n` characters (Unicode code points).

Requirements:

- If `s` is no longer than `n` characters, return `s` unchanged.
- If `n <= 0`, return an empty string.
- If truncation happens, the result must end with the ellipsis character `…`
  (U+2026) and must itself be at most `n` characters, so it contains the first
  `n-1` characters followed by the ellipsis.
- Never split a multibyte character.
- Do not change the visible tests or add dependencies.
