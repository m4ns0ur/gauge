# Count words correctly

`text.WordCount` currently splits only on spaces, so punctuation, tabs,
newlines, and Unicode text produce wrong counts. Change it so that:

- a word is a maximal run of Unicode letters or digits;
- any other character (spaces, punctuation, tabs, newlines) separates words;
- words are compared case-insensitively (`Hello` and `hello` are the same word);
- words not present in the input must not appear in the result.

Keep the existing ASCII behavior, do not change the visible tests, and do not
add dependencies.
