package indexer

// ResetForTest re-creates all package-level singleton components
// (scanner, parser, writer, symbolParser) so that Run/Stop can be
// called multiple times in the same process during testing.
// This is required because Stop() closes channels that cannot be
// reopened, and Go does not allow sending on a closed channel.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	scanner = NewScanner()
	parser = NewParser()
	writer = NewWriter()
	symbolParser = NewSymbolParser()
}
