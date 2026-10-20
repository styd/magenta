# Contributing

Thank you for helping. Bug reports, new rules, and fixes are all welcome.

## Reporting a problem

Open an issue. Run `magenta doctor` and include its output. If the problem is a wrong color or a rule that does not match, include a few lines of the input (remove anything private) and the output of `magenta detect <file>`.

## Adding a rule for a command or log

This is the most useful kind of change. In short:

1. Write `rules/<name>.toml` and check it with `magenta rule check rules/<name>.toml`.
2. Add `testdata/<name>/in.log` with realistic input, and run `go test ./internal/engine -update` to create the expected output. Read the result before you accept it.
3. Add the name to the list in `TestExpectedBuiltins` in `internal/rules`, and update the counts in `internal/magentacli`.

[Writing rules](docs/rules.md) describes the file format, and [Development](docs/development.md#adding-a-rule-file) has the full steps.

## Sending a change

Before you open a pull request, run:

```
gofmt -l .
go vet ./...
go test ./...
```

`gofmt` should print nothing. Code that touches goroutines, channels, or the terminal should also pass `go test -race ./...`. Keep each pull request to one change, and add or update a test for it.

By contributing you agree that your work is released under the [MIT license](LICENSE).
