// Package archtest holds tests that pin Arrmada's structural rules — the ones a code
// review can miss because each violation looks harmless on its own. It has no code of its
// own: each rule is a test that walks the source tree with go/parser.
package archtest
