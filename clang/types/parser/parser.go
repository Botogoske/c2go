package parser

import (
	"errors"
	"go/token"
	"go/types"
	"io"
	"log"
	"strconv"

	ctypes "github.com/goplus/c2go/clang/types"
	"github.com/goplus/c2go/clang/types/scanner"
)

var (
	ErrInvalidType = errors.New("invalid type")
)

// -----------------------------------------------------------------------------

type TypeSystem interface {
	Pkg() *types.Package
	LookupType(typ string) (t types.Type, err error)
}

const (
	FlagIsParam = 1 << iota
	FlagGetRetType
)

func isParam(flags int) bool {
	return (flags & FlagIsParam) != 0
}

func getRetType(flags int) bool {
	return (flags & FlagGetRetType) != 0
}

// qualType can be:
//   unsigned int
//   struct ConstantString
//   volatile uint32_t
//   int (*)(void *, int, char **, char **)
//   int (*)(const char *, ...)
//   int (*)(void)
//   const char *restrict
//   const char [7]
//   char *
//   void
//   ...
func ParseType(ts TypeSystem, fset *token.FileSet, qualType string, flags int) (t types.Type, isConst bool, err error) {
	p := &parser{ts: ts}
	file := fset.AddFile("", fset.Base(), len(qualType))
	p.s.Init(file, qualType, nil)

	if t, isConst, err = p.parse(flags); err != nil {
		return
	}
	if p.tok != token.EOF {
		err = p.newError("unexpect token " + p.tok.String())
	}
	return
}

// -----------------------------------------------------------------------------

type parser struct {
	s  scanner.Scanner
	ts TypeSystem

	pos token.Pos
	tok token.Token
	lit string
}

func (p *parser) next() {
	p.pos, p.tok, p.lit = p.s.Scan()
}

func (p *parser) newError(errMsg string) *ParseTypeError {
	return &ParseTypeError{QualType: p.s.Source(), Pos: p.pos, ErrMsg: errMsg}
}

func (p *parser) expect(tokExp token.Token) error {
	p.next()
	if p.tok != tokExp {
		return p.newError("expect " + tokExp.String())
	}
	return nil
}

const (
	flagShort = 1 << iota
	flagLong
	flagLongLong
	flagUnsigned
	flagSigned
)

func (p *parser) lookupType(lit string, flags int) (t types.Type, err error) {
	if flags != 0 {
		switch lit {
		case "int":
			if t = intTypes[flags&^flagSigned]; t != nil {
				return
			}
		case "char":
			switch flags {
			case flagUnsigned:
				return types.Typ[types.Uint8], nil
			case flagSigned:
				return types.Typ[types.Int8], nil
			}
		case "__int128":
			switch flags {
			case flagUnsigned:
				return ctypes.Int128, nil
			case flagSigned:
				return ctypes.Uint128, nil
			}
		}
		log.Fatalln("lookupType: TODO - invalid type")
		return nil, ErrInvalidType
	}
	return p.ts.LookupType(lit)
}

var intTypes = [...]types.Type{
	0:                                      types.Typ[types.Int],
	flagShort:                              types.Typ[types.Int16],
	flagLong:                               ctypes.Long,
	flagLong | flagLongLong:                types.Typ[types.Int64],
	flagUnsigned:                           types.Typ[types.Uint],
	flagShort | flagUnsigned:               types.Typ[types.Uint16],
	flagLong | flagUnsigned:                ctypes.Ulong,
	flagLong | flagLongLong | flagUnsigned: types.Typ[types.Uint64],
	flagShort | flagLong | flagLongLong | flagUnsigned: nil,
}

func (p *parser) parse(inFlags int) (t types.Type, isConst bool, err error) {
	flags := 0
	for {
		p.next()
	retry:
		switch p.tok {
		case token.IDENT:
		ident:
			switch p.lit {
			case "unsigned":
				flags |= flagUnsigned
			case "short":
				flags |= flagShort
			case "long":
				if (flags & flagLong) != 0 {
					flags |= flagLongLong
				} else {
					flags |= flagLong
				}
			case "signed":
				flags |= flagSigned
			case "const":
				isConst = true
			case "volatile", "restrict", "_Nullable":
			case "struct", "union":
				p.next()
				if p.tok != token.IDENT {
					log.Fatalln("c.types.ParseType: struct/union - TODO:", p.lit, "@", p.pos)
				}
				fallthrough
			default:
				if t != nil {
					return nil, false, p.newError("illegal syntax: multiple types?")
				}
				if t, err = p.lookupType(p.lit, flags); err != nil {
					return
				}
				flags = 0
			}
			if flags != 0 {
				p.next()
				if p.tok == token.IDENT {
					goto ident
				}
				if t != nil {
					return nil, false, p.newError("illegal syntax: multiple types?")
				}
				if t, err = p.lookupType("int", flags); err != nil {
					return
				}
				flags = 0
				goto retry
			}
		case token.MUL: // *
			if t == nil {
				return nil, false, p.newError("pointer to nil")
			}
			t = p.newPointer(t)
		case token.LBRACK: // [
			if t == nil {
				return nil, false, p.newError("pointer to nil")
			}
			p.next()
			if p.tok != token.INT {
				return nil, false, p.newError("array length not an integer")
			}
			n, e := strconv.Atoi(p.lit)
			if e != nil {
				return nil, false, p.newError(e.Error())
			}
			if err = p.expect(token.RBRACK); err != nil { // ]
				return
			}
			if isParam(inFlags) {
				t = p.newPointer(t)
			} else {
				t = types.NewArray(t, int64(n))
			}
		case token.LPAREN: // (
			if t == nil {
				return nil, false, p.newError("no function return type")
			}
			if err = p.expect(token.MUL); err != nil { // *
				if getRetType(inFlags) {
					err = nil
					p.tok = token.EOF
				}
				return
			}
		nextTok:
			p.next()
			switch p.tok {
			case token.RPAREN: // )
			case token.IDENT:
				if p.lit == "_Nullable" {
					goto nextTok
				}
				fallthrough
			default:
				return nil, false, p.newError("expect )")
			}
			if err = p.expect(token.LPAREN); err != nil { // (
				return
			}
			var args []*types.Var
			var results *types.Tuple
			var pkg = p.ts.Pkg()
			for {
				arg, _, e := p.parse(FlagIsParam)
				if e != nil {
					return nil, false, e
				}
				if ctypes.NotVoid(arg) {
					args = append(args, types.NewParam(token.NoPos, pkg, "", arg))
				}
				if p.tok != token.COMMA {
					break
				}
			}
			if p.tok != token.RPAREN { // )
				return nil, false, p.newError("expect )")
			}
			if ctypes.NotVoid(t) {
				results = types.NewTuple(types.NewParam(token.NoPos, pkg, "", t))
			}
			t = types.NewSignature(nil, types.NewTuple(args...), results, false)
		case token.RPAREN:
			if t == nil {
				t = ctypes.Void
			}
			return
		case token.COMMA, token.EOF:
			if t == nil {
				err = io.ErrUnexpectedEOF
			}
			return
		default:
			log.Fatalln("c.types.ParseType: unknown -", p.tok, p.lit)
		}
	}
}

func (p *parser) newPointer(t types.Type) types.Type {
	if ctypes.NotVoid(t) {
		return types.NewPointer(t)
	}
	return types.Typ[types.UnsafePointer]
}

// -----------------------------------------------------------------------------

type ParseTypeError struct {
	QualType string
	Pos      token.Pos
	ErrMsg   string
}

func (p *ParseTypeError) Error() string {
	return p.ErrMsg // TODO
}

// -----------------------------------------------------------------------------
