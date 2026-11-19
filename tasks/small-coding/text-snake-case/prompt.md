# Convert identifiers to snake_case

`text.SnakeCase` is not implemented. Implement it for ASCII identifiers:

- Split words on spaces, hyphens, and underscores.
- Lowercase every letter.
- Insert an underscore before an uppercase letter that follows a lowercase
  letter or digit (`userID` becomes `user_id`).
- Insert an underscore before the last uppercase letter of an acronym that is
  followed by a lowercase letter (`HTTPServer` becomes `http_server`).
- Collapse repeated separators and trim leading/trailing underscores.
- An empty string returns an empty string.

Do not change the visible tests or add dependencies.
