package main

import (
	"go/ast"
	"go/token"
	"strings"
)

const CRITICAL_EFFECTIVE_LINES = 3

// isNodeInRange checks if an AST node is within the specified range (considering column info)
// A node is considered "in range" if its starting position is within the block range.
// This ensures we only count nodes that actually start within the uncovered block.
func isNodeInRange(fset *token.FileSet, n ast.Node, startLine, startCol, endLine, endCol int) bool {
	pos := fset.Position(n.Pos())
	end := fset.Position(n.End())

	// Node ends before block starts
	if end.Line < startLine {
		return false
	}
	if end.Line == startLine && end.Column < startCol {
		return false
	}

	// Node starts after block ends
	if pos.Line > endLine {
		return false
	}
	if pos.Line == endLine && pos.Column > endCol {
		return false
	}

	// For better precision: node must START within the block
	// This avoids counting parent nodes (like IfStmt) when only their body is uncovered
	if pos.Line < startLine {
		return false
	}
	if pos.Line == startLine && pos.Column < startCol {
		return false
	}

	return true
}

// BlockAnalysis holds the AST analysis results for a code block
type BlockAnalysis struct {
	HasExportedFunc bool // Public API - high priority
	HasExportedType bool // Exported type methods
	HasConcurrency  bool // goroutine, channel, select
	HasErrorHandle  bool // error checking/returning
	FuncCallCount   int  // function calls
	BranchCount     int  // if, switch, for, range (cyclomatic complexity)
	FuncCount       int  // number of functions
	StatementCount  int  // executable statements
}

// Score calculates a priority score based on analysis
func (a *BlockAnalysis) Score() int {
	score := 0
	if a.HasExportedFunc {
		score += 30
	}
	if a.HasExportedType {
		score += 20
	}
	if a.HasConcurrency {
		score += 20
	}
	if a.HasErrorHandle {
		score += 10
	}
	score += a.BranchCount * 15
	score += a.FuncCallCount * 5
	score += a.FuncCount * 3
	return score
}

// AnalyzeBlockWithAST uses AST to analyze a code block and determine its priority level
func AnalyzeBlockWithAST(b *MergedBlock, astCache *ASTCache, fileCache *FileCache) {
	// First, calculate effective lines (non-empty, non-comment lines)
	// Consider column information for accurate counting
	lines, err := fileCache.GetLines(b.File)
	if err != nil {
		b.Level = "UNKNOWN"
		return
	}

	effectiveCount := 0
	for i := b.StartLine; i <= b.EndLine; i++ {
		if i < 1 || i > len(lines) {
			continue
		}

		fullLine := lines[i-1]
		var segment string

		if i == b.StartLine && i == b.EndLine {
			// Same line: only take content between StartCol and EndCol
			startIdx := min(b.StartCol-1, len(fullLine))
			endIdx := min(b.EndCol-1, len(fullLine))
			if startIdx < endIdx && startIdx >= 0 {
				segment = fullLine[startIdx:endIdx]
			}
		} else if i == b.StartLine {
			// Start line: from StartCol to end of line
			startIdx := min(b.StartCol-1, len(fullLine))
			if startIdx >= 0 && startIdx < len(fullLine) {
				segment = fullLine[startIdx:]
			}
		} else if i == b.EndLine {
			// End line: from beginning to EndCol
			endIdx := min(b.EndCol-1, len(fullLine))
			if endIdx > 0 {
				segment = fullLine[:endIdx]
			}
		} else {
			// Middle lines: entire line
			segment = fullLine
		}

		if !isLineIgnorable(strings.TrimSpace(segment)) {
			effectiveCount++
		}
	}
	b.EffectiveLines = effectiveCount

	// Parse AST and analyze
	fileAST, fset, err := astCache.GetAST(b.File)
	if err != nil {
		// Fallback to simple analysis if AST parsing fails
		b.Level = classifyByEffectiveLines(effectiveCount)
		b.FixAction = getFixAction(b.Level)
		return
	}

	analysis := analyzeASTInRange(fileAST, fset, b.StartLine, b.StartCol, b.EndLine, b.EndCol)
	score := analysis.Score()

	// Determine level based on score
	switch {
	case score >= 30 || (analysis.HasExportedFunc && effectiveCount >= CRITICAL_EFFECTIVE_LINES):
		b.Level = "CRITICAL"
		b.FixAction = "\033[31mRequired\033[0m"
	case score >= 25 || analysis.HasConcurrency:
		b.Level = "HIGH"
		b.FixAction = "Suggested"
	case score >= 10 || analysis.HasErrorHandle:
		b.Level = "MEDIUM"
		b.FixAction = "Consider"
	default:
		b.Level = "LOW"
		b.FixAction = ""
	}
}

// analyzeASTInRange walks the AST and collects information about nodes in the given line range
// It considers column information for precise node filtering
func analyzeASTInRange(file *ast.File, fset *token.FileSet, startLine, startCol, endLine, endCol int) BlockAnalysis {
	var analysis BlockAnalysis

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return true
		}

		// Check if node is within our range (considering column information)
		if !isNodeInRange(fset, n, startLine, startCol, endLine, endCol) {
			return true
		}

		switch v := n.(type) {
		case *ast.FuncDecl:
			analysis.FuncCount++
			if v.Name != nil && v.Name.IsExported() {
				analysis.HasExportedFunc = true
			}
			// Check if it's a method on an exported type
			if v.Recv != nil && len(v.Recv.List) > 0 {
				if isExportedReceiver(v.Recv.List[0].Type) {
					analysis.HasExportedType = true
				}
			}

		case *ast.IfStmt:
			analysis.BranchCount++
			// Check for error handling pattern: if err != nil
			if isErrorCheck(v.Cond) {
				analysis.HasErrorHandle = true
			}

		case *ast.ForStmt, *ast.RangeStmt:
			analysis.BranchCount++

		case *ast.SwitchStmt, *ast.TypeSwitchStmt:
			analysis.BranchCount++

		case *ast.SelectStmt:
			analysis.BranchCount++
			analysis.HasConcurrency = true

		case *ast.GoStmt:
			analysis.HasConcurrency = true

		case *ast.SendStmt: // channel send: ch <- value
			analysis.HasConcurrency = true

		case *ast.ChanType: // channel type declaration
			analysis.HasConcurrency = true

		case *ast.ReturnStmt:
			analysis.StatementCount++
			// Check if returning an error
			for _, result := range v.Results {
				if isErrorExpr(result) {
					analysis.HasErrorHandle = true
				}
			}

		case *ast.AssignStmt, *ast.ExprStmt, *ast.DeferStmt:
			analysis.StatementCount++

		case *ast.CaseClause:
			analysis.BranchCount++
		case *ast.CallExpr:
			// Check for error handling calls
			if !isErrorExpr(v.Fun) {
				analysis.FuncCallCount++
			}
		}

		return true
	})

	return analysis
}

// isExportedReceiver checks if the receiver type is exported
func isExportedReceiver(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.IsExported()
	case *ast.StarExpr: // *Type
		return isExportedReceiver(t.X)
	}
	return false
}

// isErrorCheck checks if the condition is an error check (err != nil or err == nil)
func isErrorCheck(expr ast.Expr) bool {
	binExpr, ok := expr.(*ast.BinaryExpr)
	if !ok {
		return false
	}

	// Check left side for 'err' identifier
	if ident, ok := binExpr.X.(*ast.Ident); ok {
		if strings.Contains(strings.ToLower(ident.Name), "err") {
			return true
		}
	}

	// Check right side for 'err' identifier
	if ident, ok := binExpr.Y.(*ast.Ident); ok {
		if strings.Contains(strings.ToLower(ident.Name), "err") {
			return true
		}
	}

	return false
}

// isErrorExpr checks if an expression is likely an error (named 'err' or type error)
func isErrorExpr(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok {
		name := strings.ToLower(ident.Name)
		return strings.Contains(name, "err")
	}
	// Check for fmt.Errorf, errors.New, etc.
	if call, ok := expr.(*ast.CallExpr); ok {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			funcName := sel.Sel.Name
			if funcName == "Errorf" || funcName == "New" || funcName == "Wrap" || funcName == "Wrapf" {
				return true
			}
		}
	}
	return false
}

// isInsideSelectCase checks if the code block is inside a select case (CommClause).
// Select cases handling context cancellation, channel closes, timeouts are typically
// hard to test reliably in unit tests.
func isInsideSelectCase(file *ast.File, fset *token.FileSet, startLine, endLine int) bool {
	found := false

	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		if n == nil {
			return true
		}

		// Look for CommClause (case in select statement)
		commClause, ok := n.(*ast.CommClause)
		if !ok {
			return true
		}

		// Check if our block is within this comm clause
		clauseStart := fset.Position(commClause.Pos()).Line
		clauseEnd := fset.Position(commClause.End()).Line

		// Block must be inside the comm clause body
		if startLine >= clauseStart && endLine <= clauseEnd {
			// Check if the block contains only simple statements (log/return/continue)
			// within the comm clause context
			if containsOnlySimpleStatements(commClause.Body, fset, startLine, endLine) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

// containsOnlySimpleStatements checks if the statements in a comm clause that overlap
// with the given line range are simple (return, log, continue, break)
func containsOnlySimpleStatements(stmts []ast.Stmt, fset *token.FileSet, startLine, endLine int) bool {
	for _, stmt := range stmts {
		stmtStart := fset.Position(stmt.Pos()).Line
		stmtEnd := fset.Position(stmt.End()).Line

		// Check if this statement overlaps with our block
		if stmtEnd < startLine || stmtStart > endLine {
			continue
		}

		// This statement is in our range, check if it's simple
		if !isSimpleStatement(stmt) {
			return false
		}
	}
	return true
}

// isInsideGoroutineErrorPath checks if the code block is inside a goroutine (go func())
// and contains only simple error logging/handling statements.
// Goroutine error handlers for server Start/Listen/Serve are typically hard to test.
func isInsideGoroutineErrorPath(file *ast.File, fset *token.FileSet, startLine, endLine int) bool {
	found := false

	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		if n == nil {
			return true
		}

		// Look for go statements (goroutines)
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}

		// Get the goroutine's function body
		var funcBody *ast.BlockStmt
		switch fn := goStmt.Call.Fun.(type) {
		case *ast.FuncLit:
			funcBody = fn.Body
		default:
			return true
		}

		if funcBody == nil {
			return true
		}

		// Check if our block is within this goroutine
		goStart := fset.Position(goStmt.Pos()).Line
		goEnd := fset.Position(goStmt.End()).Line

		if startLine >= goStart && endLine <= goEnd {
			// Check if the overlapping statements are simple (log/return)
			if containsOnlySimpleStatementsInBlock(funcBody, fset, startLine, endLine) {
				found = true
				return false
			}
		}

		return true
	})

	return found
}

// containsOnlySimpleStatementsInBlock checks if statements in a block that overlap
// with the given line range are simple (return, log, continue, break)
func containsOnlySimpleStatementsInBlock(block *ast.BlockStmt, fset *token.FileSet, startLine, endLine int) bool {
	if block == nil {
		return true
	}

	for _, stmt := range block.List {
		stmtStart := fset.Position(stmt.Pos()).Line
		stmtEnd := fset.Position(stmt.End()).Line

		// Check if this statement overlaps with our block
		if stmtEnd < startLine || stmtStart > endLine {
			continue
		}

		// This statement is in our range, check if it's simple
		if !isSimpleStatement(stmt) {
			return false
		}
	}
	return true
}

// isSimpleStatement checks if a statement is simple (typically used for cleanup)
func isSimpleStatement(stmt ast.Stmt) bool {
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BranchStmt: // break, continue, goto, fallthrough
		return true
	case *ast.ExprStmt:
		if call, ok := s.X.(*ast.CallExpr); ok {
			return isLoggingCall(call)
		}
		return false
	case *ast.IfStmt:
		// For if statements, check if both branches are simple
		// and the body is simple (typically: if !ok { log; return })
		bodySimple := isSimpleBlockStmt(s.Body)
		elseSimple := true
		if s.Else != nil {
			if elseBlock, ok := s.Else.(*ast.BlockStmt); ok {
				elseSimple = isSimpleBlockStmt(elseBlock)
			} else if elseIf, ok := s.Else.(*ast.IfStmt); ok {
				elseSimple = isSimpleStatement(elseIf)
			}
		}
		return bodySimple && elseSimple
	case *ast.BlockStmt:
		return isSimpleBlockStmt(s)
	default:
		return false
	}
}

// isSimpleBlockStmt checks if a block statement contains only simple statements
func isSimpleBlockStmt(block *ast.BlockStmt) bool {
	if block == nil {
		return true
	}
	for _, stmt := range block.List {
		if !isSimpleStatement(stmt) {
			return false
		}
	}
	return true
}

// isLoggingCall checks if a call expression is a logging call
func isLoggingCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		// Method call: log.Println, logger.Info, etc.
		methodName := strings.ToLower(fn.Sel.Name)
		loggingMethods := []string{
			"print", "println", "printf",
			"log", "logf",
			"info", "infof",
			"warn", "warnf", "warning",
			"error", "errorf",
			"debug", "debugf",
			"trace", "tracef",
			"fatal", "fatalf",
			"panic", "panicf",
		}
		for _, m := range loggingMethods {
			if strings.Contains(methodName, m) {
				return true
			}
		}
	case *ast.Ident:
		// Direct function call: println, print
		funcName := strings.ToLower(fn.Name)
		if funcName == "println" || funcName == "print" || funcName == "panic" {
			return true
		}
	}
	return false
}

// classifyByEffectiveLines is a fallback when AST parsing fails
func classifyByEffectiveLines(effectiveLines int) string {
	switch {
	case effectiveLines >= CRITICAL_EFFECTIVE_LINES:
		return "HIGH"
	case effectiveLines >= 2:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

// getFixAction returns the fix action string for a given level
func getFixAction(level string) string {
	switch level {
	case "CRITICAL":
		return "\033[31mRequired\033[0m"
	case "HIGH":
		return "Suggested"
	case "MEDIUM":
		return "Consider"
	default:
		return ""
	}
}

// isLineIgnorable checks if a line should be ignored in coverage analysis
func isLineIgnorable(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "//") || line == "}" || line == "){" || line == ")" || line == "]" || line == "}," {
		return true
	}
	return false
}

// AnalyzeBlock is a legacy function kept for compatibility - now deprecated
func (fc *FileCache) AnalyzeBlock(b *MergedBlock) {
	// This is kept for backward compatibility but should use AnalyzeBlockWithAST
	astCache := NewASTCache()
	AnalyzeBlockWithAST(b, astCache, fc)
}
