This is a program to collect data for https://github.com/golang/go/issues/57949

It follows the normal go tool conventions via go/packages.

It counts keyed struct literals. It specifically counts the following patterns
```
key: identifier,
key: qualified.identifier,
key: *identifier,
key: *qualified.identifier,
key: &identifier,
key: &qualified.identifier,
```
For each of these patterns it also records when `key` and `identifier` match exactly or partially. `==` is used for an exact match. A partial match are not counted for patterns with a qualified identifier and uses `x != y && strings.EqualFold(x, y)`. Partial matches are for cases such as `Title: title`.

Results are per-package followed by a total of all packages queried.