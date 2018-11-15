// Package eval contains our evaluator
//
// This is pretty simple:
//
//  * The program is an array of tokens.
//
//  * We have one statement per line.
//
//  * We handle the different types of statements in their own functions.
//
package eval

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/skx/gobasic/builtin"
	"github.com/skx/gobasic/object"
	"github.com/skx/gobasic/token"
	"github.com/skx/gobasic/tokenizer"
)

// userFunction is a structure that holds one entry for each user-defined function.
type userFunction struct {

	// name is the name of the user-defined function.
	name string

	// body is the expression to be evaluated.
	body string

	// args is the array of variable-names to set for the arguments.
	args []string
}

// Interpreter holds our state.
type Interpreter struct {

	// The program we execute is nothing more than an array of tokens.
	program []token.Token

	// Should we finish execution?
	// This is set by the `END` statement.
	finished bool

	// We execute from the given offset.
	//
	// Sequential execution just means bumping this up by one each
	// time we execute an instruction, or pick off the arguments to
	// one.
	//
	// But set it to 17, or some other random value, and you've got
	// a GOTO implemented!
	offset int

	// We record the line-number we're currently executing here.
	// NOTE: This is a string because we take it from the lineno
	// token, with no modification.
	lineno string

	// A stack for handling GOSUB/RETURN calls
	gstack *Stack

	// vars holds the variables set in the program, via LET.
	vars *Variables

	// loops holds references to open FOR-loops
	loops *Loops

	// STDIN is an input-reader used for the INPUT statement
	STDIN *bufio.Reader

	// Hack: Was the previous statement a GOTO/GOSUB?
	jump bool

	// lines is a lookup table - the key is the line-number of
	// the source program, and the value is the offset in our
	// program-array that this is located at.
	lines map[string]int

	// functions holds builtin-functions
	functions *builtin.Builtins

	// trace is true if the user is tracing execution
	trace bool

	// dataOffset keeps track of how far we've read into any
	// data-statements
	dataOffset int

	// data holds any value stored in DATA statements
	// These are populated when the program is loaded
	data []object.Object

	// fns contains a map of user-defined functions.
	fns map[string]userFunction
}

// New is our constructor.
//
// Given a lexer we store all the tokens it produced in our array, and
// initialise some other state.
func New(stream *tokenizer.Tokenizer) (*Interpreter, error) {
	t := &Interpreter{offset: 0}

	// setup a stack for holding line-numbers for GOSUB/RETURN
	t.gstack = NewStack()

	// setup storage for variable-contents
	t.vars = NewVars()

	// setup storage for for-loops
	t.loops = NewLoops()

	// Built-in functions are stored here.
	t.functions = builtin.New()

	// allow reading from STDIN
	t.STDIN = bufio.NewReader(os.Stdin)

	//
	// Setup a map to hold our jump-targets
	//
	t.lines = make(map[string]int)

	//
	// Setup a map to hold user-defined functions.
	//
	t.fns = make(map[string]userFunction)

	//
	// Save the tokens that our program consists of, one by one,
	// until we hit the end.
	//
	// We also record the offset at which each line starts, which
	// means that the GOTO & GOSUB statements don't need to scan
	// the program from start to finish to find the destination
	// to jump to.
	//
	offset := 0
	for {
		tok := stream.NextToken()
		if tok.Type == token.EOF {
			break
		}

		// Did we find a line-number?
		if tok.Type == token.LINENO {

			// Save the offset in the map
			line := tok.Literal

			// Already an offset?  That means we
			// have duplicate line-numbers
			if t.lines[line] != 0 {
				fmt.Printf("WARN: Line %s is duplicated - GOTO/GOSUB behaviour is undefined\n", line)
			}
			t.lines[line] = offset

			// TODO: Warn about line-numbers not being
			// sequential.  Or at least going-backwards.
		}

		// Regardless append the token to our array
		t.program = append(t.program, tok)

		offset++
	}

	//
	// Process the program looking for DATA statements
	//
	// For each one store the data in the line in our Data array
	//
	for offset, tok := range t.program {

		//
		// We found a data-token.
		//
		// We expect this will be "string" OR "comma" OR number.
		//
		if tok.Type == token.DATA {

			//
			// Walk the rest of the program - starting
			// from the token AFTER the DATA
			//
			start := offset + 1
			run := true

			for start < len(t.program) && run {

				tk := t.program[start]

				switch tk.Type {

				case token.NEWLINE:
					run = false
					break
				case token.COMMA:
					// NOP
				case token.STRING:
					t.data = append(t.data, &object.StringObject{Value: tk.Literal})
				case token.INT:
					i, _ := strconv.ParseFloat(tk.Literal, 64)
					t.data = append(t.data, &object.NumberObject{Value: i})
				default:
					return nil, fmt.Errorf("Error reading DATA - Unhandled token: %s", tk.String())
				}
				start++
			}
		}

		//
		// We found a user-defined function definition.
		//
		if tok.Type == token.DEF && offset >= 1 && t.program[offset-1].Type == token.LINENO {

			//
			// Parse the function-definition.
			//
			err := t.parseDefFN(offset)
			if err != nil {
				return nil, fmt.Errorf("Error in DEF FN: %s", err.Error())

			}
		}

	}

	t.dataOffset = 0

	//
	// Register our default primitives, which are implemented in the
	// builtin-package.
	//
	// We have to do this after we've loaded our program, because
	// the registration-process involves rewriting our program to
	// change "IDENT" into "BUILTIN" for each known-primitive.
	//
	// NOTE: Ideally _this_ package wouldn't know about the
	// functions which _that_ other package provides...
	//
	t.RegisterBuiltin("ABS", 1, builtin.ABS)
	t.RegisterBuiltin("ACS", 1, builtin.ACS)
	t.RegisterBuiltin("ASN", 1, builtin.ASN)
	t.RegisterBuiltin("ATN", 1, builtin.ATN)
	t.RegisterBuiltin("BIN", 1, builtin.BIN)
	t.RegisterBuiltin("COS", 1, builtin.COS)
	t.RegisterBuiltin("EXP", 1, builtin.EXP)
	t.RegisterBuiltin("INT", 1, builtin.INT)
	t.RegisterBuiltin("LN", 1, builtin.LN)
	t.RegisterBuiltin("PI", 0, builtin.PI)
	t.RegisterBuiltin("RND", 1, builtin.RND)
	t.RegisterBuiltin("SGN", 1, builtin.SGN)
	t.RegisterBuiltin("SIN", 1, builtin.SIN)
	t.RegisterBuiltin("SQR", 1, builtin.SQR)
	t.RegisterBuiltin("TAN", 1, builtin.TAN)
	t.RegisterBuiltin("VAL", 1, builtin.VAL)
	t.RegisterBuiltin("π", 0, builtin.PI)

	// Primitives that operate upon strings
	t.RegisterBuiltin("CHR$", 1, builtin.CHR)
	t.RegisterBuiltin("CODE", 1, builtin.CODE)
	t.RegisterBuiltin("LEFT$", 2, builtin.LEFT)
	t.RegisterBuiltin("LEN", 1, builtin.LEN)
	t.RegisterBuiltin("MID$", 3, builtin.MID)
	t.RegisterBuiltin("RIGHT$", 2, builtin.RIGHT)
	t.RegisterBuiltin("STR$", 1, builtin.STR)
	t.RegisterBuiltin("TL$", 1, builtin.TL)

	// Output
	t.RegisterBuiltin("PRINT", -1, builtin.PRINT)
	t.RegisterBuiltin("DUMP", 1, builtin.DUMP)

	//
	// Return our configured interpreter
	//
	return t, nil
}

// FromString is a constructor which takes a string, and constructs
// an Interpreter from it - rather than requiring the use of the tokenizer.
func FromString(input string) (*Interpreter, error) {
	tok := tokenizer.New(input)
	return New(tok)
}

////
//
// Helpers for stuff
//
////

// factor
func (e *Interpreter) factor() object.Object {

	for {

		if e.offset >= len(e.program) {
			return object.Error("Hit end of program processing factor()")
		}

		tok := e.program[e.offset]
		switch tok.Type {
		case token.LBRACKET:
			// skip past the lbracket
			e.offset++

			if e.offset >= len(e.program) {
				return object.Error("Hit end of program processing factor()")
			}

			// handle the expr
			ret := e.expr(true)
			if ret.Type() == object.ERROR {
				return ret
			}

			// skip past the rbracket
			tok = e.program[e.offset]
			if tok.Type != token.RBRACKET {
				return object.Error("Unclosed bracket around expression")
			}
			e.offset++

			// Return the result of the sub-expression
			return (ret)
		case token.INT:
			i, _ := strconv.ParseFloat(tok.Literal, 64)
			e.offset++
			return &object.NumberObject{Value: i}
		case token.STRING:
			e.offset++
			return &object.StringObject{Value: tok.Literal}
		case token.FN:

			//
			// Call the user-defined function.
			//
			e.offset++
			if e.offset >= len(e.program) {
				return object.Error("Hit end of program processing FN call")
			}

			name := e.program[e.offset]

			e.offset++
			if e.offset >= len(e.program) {
				return object.Error("Hit end of program processing FN call")
			}

			//
			// Collect the arguments.
			//
			var args []object.Object

			//
			// We walk forward collecting the values between
			// "(" & ")".
			//
			for e.offset < len(e.program) {

				tt := e.program[e.offset]

				if tt.Type == token.RBRACKET {
					e.offset++
					break
				}

				if tt.Type == token.COMMA || tt.Type == token.LBRACKET {
					e.offset++
					continue
				}

				// Call the token as an expression
				obj := e.expr(true)
				if obj.Type() == object.ERROR {
					return obj
				}
				args = append(args, obj)
			}

			//
			// Now we call the function with those values.
			//
			val := e.callUserFunction(name.Literal, args)

			return val

		case token.BUILTIN:

			//
			// Call the built-in and return the value.
			//
			val := e.callBuiltin(tok.Literal)
			return val

		case token.IDENT:

			//
			// Get the contents of the variable.
			//
			val := e.GetVariable(tok.Literal)
			e.offset++
			return val

		default:
			return object.Error("factor() - unhandled token: %v\n", tok)

		}
	}
}

// terminal - handles parsing of the form
//  ARG1 OP ARG2
//
// See also expr() which is similar.
func (e *Interpreter) term() object.Object {

	// First argument
	f1 := e.factor()

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing term()")
	}

	// Get the operator
	tok := e.program[e.offset]

	// Here we handle the obvious ones.
	for tok.Type == token.ASTERISK ||
		tok.Type == token.SLASH ||
		tok.Type == token.POW ||
		tok.Type == token.MOD {

		// skip the operator
		e.offset++

		if e.offset >= len(e.program) {
			return object.Error("Hit end of program processing term()")
		}

		// get the second argument
		f2 := e.factor()

		if e.offset >= len(e.program) {
			return object.Error("Hit end of program processing term()")
		}
		//
		// We allow operations of the form:
		//
		//  NUMBER OP NUMBER
		//
		// We can error on strings.
		//
		if f1.Type() != object.NUMBER ||
			f2.Type() != object.NUMBER {
			return object.Error("term() only handles integers")
		}

		//
		// Get the values.
		//
		v1 := f1.(*object.NumberObject).Value
		v2 := f2.(*object.NumberObject).Value

		//
		// Handle the operator.
		//
		if tok.Type == token.ASTERISK {
			f1 = &object.NumberObject{Value: v1 * v2}
		}
		if tok.Type == token.POW {
			f1 = &object.NumberObject{Value: math.Pow(v1, v2)}
		}
		if tok.Type == token.SLASH {
			if v2 == 0 {
				return object.Error("Division by zero")
			}
			f1 = &object.NumberObject{Value: v1 / v2}
		}
		if tok.Type == token.MOD {

			d1 := int(v1)
			d2 := int(v2)

			if d2 == 0 {
				return object.Error("MOD 0 is an error")
			}
			f1 = &object.NumberObject{Value: float64(d1 % d2)}
		}

		if e.offset >= len(e.program) {
			return object.Error("Hit end of program processing term()")
		}

		// repeat?
		tok = e.program[e.offset]
	}

	return f1
}

// expression - handles parsing of the form
//  ARG1 OP ARG2
// See also term() which is similar.
func (e *Interpreter) expr(allowBinOp bool) object.Object {

	// First argument.
	t1 := e.term()

	// Did this error?
	if t1.Type() == object.ERROR {
		return t1
	}

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing expr()")
	}

	// Get the operator
	tok := e.program[e.offset]

	// Here we handle the obvious ones.
	for tok.Type == token.PLUS ||
		tok.Type == token.MINUS ||
		tok.Type == token.AND ||
		tok.Type == token.OR ||
		tok.Type == token.XOR {

		//
		// Sometimes we disable binary AND + binary OR.
		//
		// This is mostly due to our naive parser, because
		// it gets confused handling "IF BLAH AND BLAH  .."
		//
		if allowBinOp == false {
			if tok.Type == token.AND ||
				tok.Type == token.OR ||
				tok.Type == token.XOR {
				return t1
			}
		}

		// skip the operator
		e.offset++

		// Get the second argument.
		t2 := e.term()

		// Did this error?
		if t2.Type() == object.ERROR {
			return t2
		}

		//
		// We allow operations of the form:
		//
		//  NUMBER OP NUMBER
		//
		//  STRING OP STRING
		//
		// We support ZERO operations where the operand types
		// do not match.  If we hit this it's a bug.
		//
		if t1.Type() != t2.Type() {
			return object.Error("expr() - type mismatch between '%v' + '%v'", t1, t2)
		}

		//
		// Are the operands strings?
		//
		if t1.Type() == object.STRING {

			//
			// Get their values.
			//
			s1 := t1.(*object.StringObject).Value
			s2 := t2.(*object.StringObject).Value

			//
			// We only support "+" for concatenation
			//
			if tok.Type == token.PLUS {
				t1 = &object.StringObject{Value: s1 + s2}
			} else {
				return object.Error("expr() operation '%s' not supported for strings", tok.Literal)
			}

		} else {

			//
			// Here we have two operands that are numbers.
			//
			// Get their values for neatness.
			//
			n1 := t1.(*object.NumberObject).Value
			n2 := t2.(*object.NumberObject).Value

			if tok.Type == token.PLUS {
				t1 = &object.NumberObject{Value: n1 + n2}
			} else if tok.Type == token.MINUS {
				t1 = &object.NumberObject{Value: n1 - n2}
			} else if tok.Type == token.AND {
				t1 = &object.NumberObject{Value: float64(int(n1) & int(n2))}
			} else if tok.Type == token.OR {
				t1 = &object.NumberObject{Value: float64(int(n1) | int(n2))}
			} else if tok.Type == token.XOR {
				t1 = &object.NumberObject{Value: float64(int(n1) ^ int(n2))}
			} else {
				return object.Error("Token not handled for two numbers: %s\n", tok.Literal)
			}
		}

		// repeat?
		tok = e.program[e.offset]
	}

	return t1
}

// compare runs a comparison function (!)
//
// It is only used by the `IF` statement.
func (e *Interpreter) compare(allowBinOp bool) object.Object {

	// Get the first statement
	t1 := e.expr(allowBinOp)
	if t1.Type() == object.ERROR {
		return t1
	}

	// Get the comparison function
	op := e.program[e.offset]

	// If the next token is an THEN then we're going
	// to regard the test as a pass if the first
	// value was not 0 (number) and not "" (string)
	if op.Type == token.THEN {

		switch t1.Type() {
		case object.STRING:
			if "" != t1.(*object.StringObject).Value {
				return &object.NumberObject{Value: 1}
			}
		case object.NUMBER:
			if 0 != t1.(*object.NumberObject).Value {
				return &object.NumberObject{Value: 1}
			}
		}
		return &object.NumberObject{Value: 0}
	}

	//
	// OK bump past the comparison function.
	//
	e.offset++

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing compare()")
	}

	// Get the second expression
	t2 := e.expr(allowBinOp)
	if t2.Type() == object.ERROR {
		return t2
	}

	//
	// String-tests here
	//
	if t1.Type() == object.STRING && t2.Type() == object.STRING {

		v1 := t1.(*object.StringObject).Value
		v2 := t2.(*object.StringObject).Value

		switch op.Type {
		case token.ASSIGN:
			if v1 == v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.NOTEQUALS:
			if v1 != v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.GT:
			if v1 > v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.GTEQUALS:
			if v1 >= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.LT:
			if v1 < v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.LTEQUALS:
			if v1 <= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		}
		// false
		return &object.NumberObject{Value: 0}
	}

	//
	// Number-tests here
	//
	if t1.Type() == object.NUMBER && t2.Type() == object.NUMBER {

		v1 := t1.(*object.NumberObject).Value
		v2 := t2.(*object.NumberObject).Value

		switch op.Type {
		case token.ASSIGN:
			if v1 == v2 {
				//true
				return &object.NumberObject{Value: 1}
			}

		case token.GT:
			if v1 > v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.GTEQUALS:
			if v1 >= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.LT:
			if v1 < v2 {
				//true
				return &object.NumberObject{Value: 1}
			}

		case token.LTEQUALS:
			if v1 <= v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		case token.NOTEQUALS:
			if v1 != v2 {
				//true
				return &object.NumberObject{Value: 1}
			}
		}
	}

	// false
	return &object.NumberObject{Value: 0}

}

// parseDefFN is an internal function invoked at the time
// a program is loaded.
func (e *Interpreter) parseDefFN(offset int) error {

	// The general form of a function-definition is
	//    DEF FN NAME ( [ARG, COMMA] ) = "BLAH BLAH"
	//

	// skip past the DEF
	offset++
	if offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing DEF FN")
	}

	// Next token should be "FN"
	fn := e.program[offset]
	if fn.Type != token.FN {
		return (fmt.Errorf("Expected FN after DEF"))
	}
	offset++
	if offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing DEF FN")
	}

	// Now a name
	name := e.program[offset]
	if name.Type != token.IDENT {
		return (fmt.Errorf("Expected function-name after 'DEF FN', got %s", name.String()))
	}
	offset++
	if offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing DEF FN")
	}

	// Now an opening parenthesis.
	open := e.program[offset]
	if open.Type != token.LBRACKET {
		return (fmt.Errorf("Expected ( after 'DEF FN %s'", name))
	}
	offset++
	if offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing DEF FN")
	}

	//
	// Collect the names of each variable which is an argument
	//
	// We loop "forever", skipping commas, and keeping going until
	// we find the closing bracket.
	//
	var args []string

	//
	// Loop "forever".
	//
	for offset < len(e.program) {

		// Get the next token
		tt := e.program[offset]

		// Is it a bracket?  If so skip it, and we're done.
		if tt.Type == token.RBRACKET {
			offset++
			break
		}

		// Is it a comma?  Then skip it
		if tt.Type == token.COMMA {
			offset++
			continue
		}

		// Otherwise we'll assume we have an ID.
		// Anything else is an error.
		if tt.Type != token.IDENT {
			return (fmt.Errorf("Unexpected token %s in DEF FN %s", tt.String(), name))
		}

		//
		// Save the ID in our array, and move on to the next
		// token.
		//
		args = append(args, tt.Literal)
		offset++
	}

	//
	// Ensure we've still got tokens.
	//
	if offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing DEF FN")
	}

	//
	// At this point we should have a token which is "="
	//
	eq := e.program[offset]
	offset++
	if eq.Type != token.ASSIGN {
		return (fmt.Errorf("Expected = after 'DEF FN %s(%s) - Got %s", name, strings.Join(args, ","), eq.String()))
	}

	//
	// Build up the body of the function.
	//
	body := ""
	for offset < len(e.program) {
		tok := e.program[offset]
		if tok.Type == token.NEWLINE {
			break
		}

		body += " "

		// Quote strings.  Sigh
		if tok.Type == token.STRING {
			body += "\""
			body += tok.Literal
			body += "\""
		} else {
			body += tok.Literal
		}
		offset++
	}

	//
	// Empty body?
	//
	if body == "" {
		return fmt.Errorf("Hit end of program processing DEF FN")
	}

	//
	// Store the definition in our map.
	//
	// This will let it be called, by name.
	//
	e.fns[name.Literal] = userFunction{name: name.Literal, body: body, args: args}
	return nil
}

// callUserFunction calls the specified user-defined function.
func (e *Interpreter) callUserFunction(name string, args []object.Object) object.Object {

	//
	// Helpful debugging.
	//
	if e.trace {
		fmt.Printf("Calling user-defined function %s\n", name)
	}

	//
	// Lookup the function; if it isn't defined then we can't invoke
	// it, obviously!
	//
	fun := e.fns[name]
	if fun.name == "" {
		return object.Error("User-defined function %s doesn't exist", name)
	}

	//
	// Does the argument count supplied and parameter count match?
	//
	if len(fun.args) != len(args) {
		return object.Error("Argument count mis-match")
	}

	//
	// OK we're essentially having to implement an `eval` function
	// to process the body of the user-defined function.
	//
	// That means we need to create a temporary tokenizer to
	// read the body, then evaluate on that second copy.
	//
	// Create the tokenizer, and the evaluator which use it.
	//
	// Note without this trailing newline we hit an error:
	//   	Hit end of program processing term()
	//
	// TODO: Fix this, it is obviously a BUG.
	//
	tokenizer := tokenizer.New(fun.body + "\n")
	eval, err := New(tokenizer)
	if err != nil {
		return object.Error(err.Error())
	}

	//
	// The new instance won't have any variables setup, but that's
	// OK.  The expression will only refer to the arguments it was
	// given by name.
	//
	// Populate the variables in the environment of our (child) evaluater.
	//
	for i := range args {
		if e.trace {
			fmt.Printf("Setting %s -> %s\n", fun.args[i], args[i].String())
		}
		eval.SetVariable(fun.args[i], args[i])
	}

	//
	// Now we can evaluate the expression in the context of this
	// child-evaluator.
	//
	out := eval.expr(true)
	if e.trace {
		fmt.Printf("\tCalled expr() - result is\n\t%s\n", out.String())
	}

	// Return the value.
	return (out)
}

// Call the built-in with the given name if we can.
func (e *Interpreter) callBuiltin(name string) object.Object {

	if e.trace {
		fmt.Printf("callBultin(%s)\n", name)
	}

	//
	// Fetch the function, so we know how many arguments
	// it should expect.
	//
	n, fun := e.functions.Get(name)

	//
	// skip past the function-call itself
	//
	e.offset++

	if e.offset >= len(e.program) {
		return object.Error("Hit end of program processing builtin %s", name)
	}

	//
	// Each built-in takes a specific number of arguments.
	//
	// We pass only `string` or `number` to it.
	//
	var args []object.Object

	//
	// Build up the args, converting and evaluating as we go.
	//
	for n == -1 || len(args) < n {

		if e.offset >= len(e.program) {
			return object.Error("Hit end of program processing builtin %s", name)
		}

		//
		// Get the next token, if it is a comma then eat it.
		//
		tok := e.program[e.offset]
		if tok.Type == token.COMMA || tok.Type == token.SEMICOLON {

			//
			// Hack
			//
			if name == "PRINT" || name == "print" {

				args = append(args, &object.StringObject{Value: " "})
			}
			e.offset++
			continue
		}

		//
		// If we've hit a colon, or a newline we're done.
		//

		//
		// If we hit newline/eof then we're done.
		//
		// (And we've got an error, because we didn't receive as
		// many arguments as we expected.)
		//
		if tok.Type == token.NEWLINE {
			if n > 0 {
				return (object.Error("Hit newline while searching for argument %d to %s", len(args)+1, name))
			}
			break
		}
		if tok.Type == token.COLON {
			if n > 0 {
				return (object.Error("Hit ':' while searching for argument %d to %s", len(args)+1, name))
			}
			break
		}
		if tok.Type == token.EOF {
			if n > 0 {
				return (object.Error("Hit EOF while searching for argument %d to %s", len(args)+1, name))
			}
			break
		}

		//
		// Evaluate the next expression.
		//
		obj := e.expr(true)

		//
		// If we found an error then return it.
		//
		if obj.Type() == object.ERROR {
			return obj
		}

		//
		// Append the argument to our list.
		//
		args = append(args, obj)

		//
		// Show our current progress.
		//
		if e.trace {
			fmt.Printf("\tArgument %d -> %s\n", len(args), obj.String())
		}
	}

	//
	// Actually call the function, now we have the correct number
	// of arguments to do so.
	//
	out := fun(e, args)

	if e.trace {
		fmt.Printf("\tReturn value %s\n", out.String())
	}
	return out
}

////
//
// Statement-handlers
//
////

// runForLoop handles a FOR loop
func (e *Interpreter) runForLoop() error {
	// we expect "FOR VAR = START to END [STEP EXPR]"

	// Bump past the FOR token
	e.offset++

	// Ensure we've not walked off the end of the program.
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing FOR")
	}

	// We now expect a variable name.
	target := e.program[e.offset]
	if target.Type != token.IDENT {
		return fmt.Errorf("Expected IDENT after FOR, got %v", target)
	}
	e.offset++
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing FOR")
	}

	// Now an EQUALS
	eq := e.program[e.offset]
	if eq.Type != token.ASSIGN {
		return fmt.Errorf("Expected = after 'FOR %s' , got %v", target.Literal, eq)
	}
	e.offset++
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing FOR")
	}

	// Now an integer/variable
	startI := e.program[e.offset]
	e.offset++
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing FOR")
	}

	var start float64
	if startI.Type == token.INT {
		v, _ := strconv.ParseFloat(startI.Literal, 64)
		start = v
	} else if startI.Type == token.IDENT {

		x := e.GetVariable(startI.Literal)
		if x.Type() != object.NUMBER {
			return fmt.Errorf("FOR: start-variable must be an integer")
		}
		start = x.(*object.NumberObject).Value
	} else {
		return fmt.Errorf("Expected INT/VARIABLE after 'FOR %s=', got %v", target.Literal, startI)
	}

	// Now TO
	to := e.program[e.offset]
	if to.Type != token.TO {
		return fmt.Errorf("Expected TO after 'FOR %s=%s', got %v", target.Literal, startI, to)
	}

	e.offset++
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing FOR")
	}

	// Now an integer/variable
	endI := e.program[e.offset]

	var end float64

	if endI.Type == token.INT {
		v, _ := strconv.ParseFloat(endI.Literal, 64)
		end = v
	} else if endI.Type == token.IDENT {

		x := e.GetVariable(endI.Literal)
		if x.Type() != object.NUMBER {
			return fmt.Errorf("FOR: end-variable must be an integer")
		}
		end = x.(*object.NumberObject).Value
	} else {
		return fmt.Errorf("Expected INT/VARIABLE after 'FOR %s=%s TO', got %v", target.Literal, startI, endI)
	}

	// Default step is 1.
	step := 1.0

	e.offset++
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing FOR")
	}

	// Is the next token a step?
	if e.program[e.offset].Type == token.STEP {

		// Skip past the step
		e.offset++

		if e.offset >= len(e.program) {
			return fmt.Errorf("Hit end of program processing FOR")
		}

		// Parse the STEP-expression.
		s := e.expr(true)

		if s.Type() != object.NUMBER {
			return fmt.Errorf("FOR loops expect an integer STEP, got %s", s.String())
		}
		step = s.(*object.NumberObject).Value
	}

	//
	// Now we can record the important details of the for-loop
	// in a hash.
	//
	// The key observersions here are that all the magic
	// really involved in the FOR-loop happens at the point
	// you interpret the "NEXT X" statement.
	//
	// Handling the NEXT statement involves:
	//
	//  Incrementing the step-variable
	//  Looking for termination
	//  If not over then "jumping back".
	//
	// So for a for-loop we just record the start/end conditions
	// and the address of the body of the loop - ie. the next
	// token - so that the next-handler can GOTO there.
	//
	// It is almost beautifully elegent.
	//
	f := ForLoop{id: target.Literal,
		offset: e.offset,
		start:  start,
		end:    end,
		step:   step}

	//
	// Set the variable to the starting-value
	//
	e.SetVariable(target.Literal, &object.NumberObject{Value: start})

	//
	// And record our loop - keyed on the name of the variable
	// which is used as the index.  This allows easy and natural
	// nested-loops.
	//
	// Did I say this is elegent?
	//
	e.loops.Add(f)
	return nil
}

// runGOSUB handles a control-flow change
func (e *Interpreter) runGOSUB() error {

	// Skip the GOSUB-instruction itself
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing GOSUB")
	}

	// Get the target
	target := e.program[e.offset]

	// We expect the target to be an int
	if target.Type != token.INT {
		return (fmt.Errorf("ERROR: GOSUB should be followed by an integer"))
	}

	//
	// We want to store the return address on our GOSUB-stack,
	// so that the next RETURN will continue execution at the
	// next instruction.
	//
	// Because we only support one statement per-line we can
	// handle that by bumping forward.  That should put us on the
	// LINENO of the following-line.
	//
	e.offset++
	e.gstack.Push(e.offset)

	//
	// Lookup the offset of the given line-number in our program.
	//
	offset := e.lines[target.Literal]

	//
	// If we found it then use it.
	//
	// If we didn't find the line then we'll continue
	// execution at the last line.
	//
	// This is strictly speaking a bug, but there is
	// the chance this is OK.
	//
	//
	if offset >= 0 {
		e.offset = offset
	}
	return nil
}

// runGOTO handles a control-flow change
func (e *Interpreter) runGOTO() error {

	// Skip the GOTO-instruction
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing GOTO")
	}

	// Get the GOTO-target
	target := e.program[e.offset]

	// We expect the target to be an int
	if target.Type != token.INT {
		return fmt.Errorf("ERROR: GOTO should be followed by an integer")
	}

	//
	// Lookup the offset of the given line-number in our program.
	//
	offset := e.lines[target.Literal]

	//
	// If we found it then use it.
	//
	// If we didn't find the line then we'll continue
	// execution at the last line.
	//
	// This is strictly speaking a bug, but there is
	// the chance this is OK.
	//
	if offset >= 0 {
		e.offset = offset
	}

	return nil
}

// runINPUT handles input of numbers from the user.
//
// NOTE:
//   INPUT "Foo", a   -> Reads an integer
//   INPUT "Foo", a$  -> Reads a string
func (e *Interpreter) runINPUT() error {

	// Skip the INPUT-instruction
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// Get the prompt
	prompt := e.program[e.offset]
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// We expect a comma
	comma := e.program[e.offset]
	e.offset++
	if comma.Type != token.COMMA {
		return fmt.Errorf("ERROR: INPUT should be : INPUT \"prompt\",var")
	}

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing INPUT")
	}

	// Now the ID
	ident := e.program[e.offset]
	e.offset++
	if ident.Type != token.IDENT {
		return fmt.Errorf("ERROR: INPUT should be : INPUT \"prompt\",var")
	}

	p := ""

	//
	// Print the prompt
	//
	switch prompt.Type {
	case token.STRING:
		p = prompt.Literal
	case token.IDENT:
		// We'll print the contents of a variable
		// if it is a string.
		value := e.GetVariable(prompt.Literal)
		if value.Type() != object.STRING {
			return fmt.Errorf("INPUT only handles string-prompts")
		}
		p = value.(*object.StringObject).Value
	default:
		return fmt.Errorf("INPUT invalid prompt-type %s", prompt.String())
	}
	fmt.Printf("%s", p)

	//
	// Read the input from the user.
	//
	var input string

	if flag.Lookup("test.v") == nil {
		input, _ = e.STDIN.ReadString('\n')
	} else {
		//
		// This is horrid
		//
		// If prompt contains "string" we return a string
		//
		// If prompt contains "number" we return a number
		//
		if strings.Contains(p, "string") {
			input = "steve"
		}
		if strings.Contains(p, "number") {
			input = "3.21"
		}

	}
	input = strings.TrimRight(input, "\n")

	//
	// Now we handle the type-conversion.
	//
	if strings.HasSuffix(ident.Literal, "$") {
		// We set a string
		e.SetVariable(ident.Literal, &object.StringObject{Value: input})
		return nil
	}

	//
	// Set the value
	//
	i, _ := strconv.ParseFloat(input, 64)
	e.SetVariable(ident.Literal, &object.NumberObject{Value: i})
	return nil
}

// runIF handles conditional testing.
//
// There are a lot of choices to be made when it comes to IF, such as
// whether to support an ELSE section or not.  And what to allow
// inside the matching section generally:
//
// A single statement?
// A block?
//
// Here we _only_ allow:
//
//  IF $EXPR THEN $STATEMENT ELSE $STATEMENT NEWLINE
//
// $STATEMENT will only be a single expression
//
func (e *Interpreter) runIF() error {

	// Bump past the IF token
	e.offset++

	// Get the result of the comparison-function
	// against the two arguments.
	res := e.compare(false)

	// Error?
	if res.Type() == object.ERROR {
		return fmt.Errorf("%s", res.(*object.ErrorObject).Value)
	}

	//
	// We need a boolean result, so we convert here.
	//
	result := false
	if res.Type() == object.NUMBER {
		result = (res.(*object.NumberObject).Value == 1)
	}

	//
	// The general form of an IF statement is
	//  IF $COMPARE THEN .. ELSE .. NEWLINE
	//
	// However we also want to allow people to write:
	//
	//  IF A=3 OR A=4 THEN ..
	//
	// So we'll special case things here.
	//

	// We now expect THEN most of the time
	target := e.program[e.offset]
	e.offset++

	for target.Type == token.AND ||
		target.Type == token.OR ||
		target.Type == token.XOR {

		//
		// See what the next comparison looks like.
		//
		extra := e.compare(false)

		if extra.Type() == object.ERROR {
			return fmt.Errorf("%s", extra.(*object.ErrorObject).Value)
		}

		//
		// We need a boolean answer.
		//
		extraResult := false
		if extra.Type() == object.NUMBER {
			extraResult = (extra.(*object.NumberObject).Value == 1)
		}

		//
		// Update our result appropriately.
		//
		if target.Type == token.AND {
			result = result && extraResult
		}
		if target.Type == token.OR {
			result = result || extraResult
		}
		if target.Type == token.XOR {
			if result != extraResult {
				result = false
			} else {
				result = true
			}
		}

		// Repeat?
		target = e.program[e.offset]
		e.offset++
	}

	//
	// Now we're in the THEN section.
	//
	if target.Type != token.THEN {
		return fmt.Errorf("Expected THEN after IF EXPR, got %v", target)
	}

	//
	// OK so if our comparison succeeded we can execute the single
	// statement between THEN + ELSE
	//
	// Otherwise between ELSE + Newline
	//
	if result {

		//
		// Execute single statement
		//
		e.RunOnce()

		//
		// Help me, I'm in Hell.
		//
		e.offset--

		//
		// If the user made a jump then we'll
		// abort here, because if the single-statement modified our
		// flow control we're screwed.
		//
		// (Because we'll start searching from the NEW location.)
		//
		//
		if e.jump {
			return nil
		}

		//
		// At this point we've had code like this:
		//
		//   IF [true] THEN [action]
		//
		// Our current offset points to action-1.
		//
		// We want to skip rest of the line, because we might have:
		//
		//   IF [ true ]; then [action] ELSE [run me..]
		//
		return (e.swallowLine())
	} else {

		//
		// When we hit this block we've hit a condition
		// that failed.
		//
		// So we want to jump to the ELSE part of the line:
		//
		//   IF [false] THEN [action] ELSE [run me..]
		//
		//
		//
		run := true

		for e.offset < len(e.program) && run {

			tmp := e.program[e.offset]
			e.offset++

			// If we hit the newline then we're done
			if tmp.Type == token.NEWLINE {
				run = false
				continue
			}

			// Otherwise did we hit the else?
			if tmp.Type == token.ELSE {

				// Execute the single statement
				e.RunOnce()

				// Then terminate.
				run = false
			}
		}
	}

	return nil
}

// runLET handles variable creation/updating.
func (e *Interpreter) runLET() error {

	// Bump past the LET token
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing LET")
	}

	// We now expect an ID
	target := e.program[e.offset]

	e.offset++
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing LET")
	}
	if target.Type != token.IDENT {
		return fmt.Errorf("Expected IDENT after LET, got %v", target)
	}

	// Now "="
	assign := e.program[e.offset]
	if assign.Type != token.ASSIGN {
		return fmt.Errorf("Expected assignment after LET x, got %v", assign)
	}
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing LET")
	}
	// now we're at the expression/value/whatever
	res := e.expr(true)

	// Did we get an error in the expression?
	if res.Type() == object.ERROR {
		return fmt.Errorf("%s", res.(*object.ErrorObject).Value)
	}

	// Store the result
	e.SetVariable(target.Literal, res)
	return nil
}

// runNEXT handles the NEXT statement
func (e *Interpreter) runNEXT() error {

	// Bump past the NEXT token
	e.offset++

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing NEXT")
	}

	// Get the identifier
	target := e.program[e.offset]
	e.offset++
	if target.Type != token.IDENT {
		return fmt.Errorf("Expected IDENT after NEXT in FOR loop, got %v", target)
	}

	// OK we've found the tail of a loop
	//
	// We need to bump the value of the given variable by the offset
	// and compare it against the max.
	//
	// If the max hasn't finished we loop around again.
	//
	// If it has we remove the for-loop
	//
	data := e.loops.Get(target.Literal)
	if data.id == "" {
		return fmt.Errorf("NEXT %s found - without opening FOR", target.Literal)
	}

	//
	// Get the variable value, and increase it.
	//
	cur := e.GetVariable(target.Literal)
	if cur.Type() != object.NUMBER {
		return fmt.Errorf("NEXT variable %s is not a number", target.Literal)
	}
	iVal := cur.(*object.NumberObject).Value

	//
	// If the start/end offsets are the same then
	// we terminate immediately.
	//
	if data.start == data.end {
		data.finished = true

		// updates-in-place.  bad name
		e.loops.Add(data)
	}

	//
	// Increment the number.
	//
	iVal += float64(data.step)

	//
	// Truncation here, to ensure that float-imprecision doesn't
	// cause any surprises.
	//
	iVal = float64(int(iVal*100)) / 100

	//
	// Set it
	//
	e.SetVariable(target.Literal, &object.NumberObject{Value: iVal})

	//
	// Have we finnished?
	//
	if data.finished {
		e.loops.Remove(target.Literal)
		return nil
	}

	//
	// If we've reached our limit we mark this as complete,
	// but note that we dont' terminate to allow the actual
	// end-number to be inclusive.
	//
	if iVal == float64(data.end) {
		data.finished = true

		// updates-in-place.  bad name
		e.loops.Add(data)
	}

	//
	// Otherwise loop again
	//
	e.offset = data.offset
	return nil
}

// Swallow all input until the following newline / EOF.
//
// This is used by:
//
//  REM
//  DATA
//  DEF FN
//
func (e *Interpreter) swallowLine() error {

	run := true

	for e.offset < len(e.program) && run {
		tok := e.program[e.offset]
		if tok.Type == token.NEWLINE {
			run = false
		}
		e.offset++
	}

	return nil
}

// READ handles reading data from the embedded DATA statements in our
// program.
func (e *Interpreter) runREAD() error {

	//
	// Skip the DATA statement
	//
	e.offset++

	//
	// Ensure we don't walk off the end of our program.
	//
	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing DATA")
	}

	//
	// We assume we're invoked with an arbitrary number
	// of tokens - each of which is a variable name.
	//
	run := true
	for e.offset < len(e.program) && run {

		// Get the token.
		tok := e.program[e.offset]

		// Have we hit the end of the line?  If so we set `run`
		// to be false, which means we hit the `return nil` at the
		// end of the function.
		//
		// If we just returned here we'd miss testing-coverage
		// of that final return.  Testing is hard!
		//
		if tok.Type == token.NEWLINE {
			run = false
			continue
		}

		// Comma?
		if tok.Type == token.COMMA {
			e.offset++
			continue
		}

		// OK that just leaves IDENT
		if tok.Type != token.IDENT {
			return (fmt.Errorf("Expected identifier after DATA - found %s", tok.String()))
		}

		//
		// OK here we set the value to the appropriate index
		// in our data-statement - first of all make sure we've
		// not read too much.
		//
		if e.dataOffset >= len(e.data) {
			return fmt.Errorf("Read past the end of our DATA storage - length %d", len(e.data))
		}

		//
		// Set the value, and bump our index
		//
		e.SetVariable(tok.Literal, e.data[e.dataOffset])
		e.dataOffset++

		//
		// Repeat for more variables
		//
		e.offset++
	}

	return nil
}

// RETURN handles a control-flow operation
func (e *Interpreter) runRETURN() error {

	// Stack can't be empty
	if e.gstack.Empty() {
		return fmt.Errorf("RETURN without GOSUB")
	}

	// Get the return address
	ret, _ := e.gstack.Pop()

	// Return execution where we left off.
	e.offset = ret
	return nil
}

////
//
// Our core public API
//
////

// RunOnce executes a single statement.
func (e *Interpreter) RunOnce() error {

	if e.offset >= len(e.program) {
		return fmt.Errorf("Hit end of program processing RunOnce()")
	}

	//
	// Get the current token
	//
	tok := e.program[e.offset]
	var err error

	if e.trace {
		fmt.Printf("RunOnce( %s )\n", tok.String())
	}

	e.jump = false

	//
	// Handle this token
	//
	switch tok.Type {
	case token.NEWLINE:
		// NOP
	case token.LINENO:
		e.lineno = tok.Literal
	case token.END:
		e.finished = true
		return nil
	case token.FOR:
		err = e.runForLoop()
	case token.GOSUB:
		err = e.runGOSUB()
		e.jump = true
	case token.GOTO:
		err = e.runGOTO()
		e.jump = true
	case token.INPUT:
		err = e.runINPUT()
	case token.IF:
		err = e.runIF()
	case token.LET:
		err = e.runLET()
	case token.NEXT:
		err = e.runNEXT()
	case token.REM:
		err = e.swallowLine()
	case token.RETURN:
		err = e.runRETURN()
	case token.DATA:
		err = e.swallowLine()
	case token.READ:
		err = e.runREAD()
	case token.DEF:
		err = e.swallowLine()
	case token.BUILTIN:

		obj := e.callBuiltin(tok.Literal)

		if obj.Type() == object.ERROR {
			return fmt.Errorf("%s", obj.(*object.ErrorObject).Value)
		}

		e.offset--
	default:
		//
		// This is either a clever piece of code, or a terrible
		// idea.
		//
		// Evaluate anything remaining, and throw away the result.
		//
		// Having this here allows side-effects via user-defined
		// functions:
		//
		//    10 DEF FN steve() = PRINT "Hello, world\n"
		//    20 FN steve()
		//
		e.expr(true)
	}

	//
	// Ready for the next instruction
	//
	e.offset++

	//
	// Error?
	//
	if err != nil {
		return err
	}
	return nil
}

// Run launches the program, and does not return until it is over.
//
// A program will terminate when the control reaches the end of the
// final-line, or when the "END" token is encountered.
func (e *Interpreter) Run() error {

	//
	// We walk our series of tokens.
	//
	for e.offset < len(e.program) && !e.finished {

		err := e.RunOnce()

		if err != nil {
			return fmt.Errorf("Line %s : %s", e.lineno, err.Error())
		}
	}

	//
	// Here we've finished with no error, but we want to
	// alert on unclosed FOR-loops.
	//
	if !e.loops.Empty() {
		return fmt.Errorf("Unclosed FOR loop")
	}

	return nil
}

// SetTrace allows the user to enable output of debugging-information
// to STDOUT when the intepreter is running.
func (e *Interpreter) SetTrace(val bool) {
	e.trace = val
}

// GetTrace returns a boolean result indicating whether debugging information
// is output to STDOUT during the course of execution.
func (e *Interpreter) GetTrace() bool {
	return (e.trace)
}

// SetVariable sets the contents of a variable in the interpreter environment.
//
// Useful for testing/embedding.
//
func (e *Interpreter) SetVariable(id string, val object.Object) {
	e.vars.Set(id, val)
}

// GetVariable returns the contents of the given variable.
//
// Useful for testing/embedding.
//
func (e *Interpreter) GetVariable(id string) object.Object {

	val := e.vars.Get(id)
	if val != nil {
		return val
	}
	return object.Error("The variable '%s' doesn't exist", id)
}

// RegisterBuiltin registers a function as a built-in, so that it can
// be called from the users' BASIC program.
//
// Useful for embedding.
//
func (e *Interpreter) RegisterBuiltin(name string, nArgs int, ft builtin.Signature) {

	//
	// We want to make sure that we handle both of these:
	//
	//   10 print "OK\n"
	//   10 PRINT "OK\n"
	//
	// Users who use mixed-case will find surprises though!
	//
	lName := strings.ToLower(name)
	uName := strings.ToUpper(name)

	// Register the built-in - both lower-case and upper-case
	e.functions.Register(lName, nArgs, ft)
	e.functions.Register(uName, nArgs, ft)

	// Now ensure that in the future if we hit this built-in
	// we regard it as a function-call, not a variable
	for i := 0; i < len(e.program); i++ {

		// Is this token a reference to the function
		// as an ident?
		if e.program[i].Type == token.IDENT &&
			(e.program[i].Literal == lName ||
				e.program[i].Literal == uName) {

			// Change the type of the token.
			e.program[i].Type = token.BUILTIN
		}
	}
}
