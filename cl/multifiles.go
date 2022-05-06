package cl

import (
	"log"
	"path/filepath"
	"strconv"

	ctypes "github.com/goplus/c2go/clang/types"

	"github.com/goplus/c2go/clang/ast"
	"github.com/goplus/gox"
)

type multiFileCtl struct {
	PkgInfo
	exists   map[string]none // only valid on hasMulti
	base     *int            // anonymous struct/union
	hasMulti bool
	inHeader bool // only valid on hasMulti
}

func (p *multiFileCtl) initMultiFileCtl(pkg *gox.Package, conf *Config) {
	if reused := conf.Reused; reused != nil {
		pi := reused.pkg.pi
		if pi == nil {
			pi = new(PkgInfo)
			pi.typdecls = make(map[string]*gox.TypeDecl)
			pi.extfns = make(map[string]none)
			reused.pkg.pi = pi
			reused.pkg.Package = pkg
		}
		p.typdecls = pi.typdecls
		p.extfns = pi.extfns
		if reused.exists == nil {
			reused.exists = make(map[string]none)
		}
		p.exists = reused.exists
		p.base = &reused.base
		p.hasMulti = true
	} else {
		p.typdecls = make(map[string]*gox.TypeDecl)
		p.extfns = make(map[string]none)
		p.base = new(int)
	}
}

const (
	suNormal = iota
	suAnonymous
)

func (p *blockCtx) getSuName(v *ast.Node, tag string) (string, int) {
	if name := v.Name; name != "" {
		return ctypes.MangledName(tag, name), suNormal
	}
	*p.base++
	return "_cgoa_" + strconv.Itoa(*p.base), suAnonymous
}

func (p *blockCtx) autoStaticName(name string) string {
	*p.base++
	return name + "_cgo" + strconv.Itoa(*p.base)
}

func (p *blockCtx) logFile(node *ast.Node) {
	if f := node.Loc.PresumedFile; f != "" {
		if debugCompileDecl {
			log.Println("==>", f)
		}
		if p.hasMulti {
			var fname string
			switch filepath.Ext(f) {
			case ".c":
				fname = filepath.Base(f) + ".i.go"
				p.inHeader = false
			default:
				fname = headerGoFile
				p.inHeader = true
			}
			p.pkg.SetCurFile(fname, true)
		}
	}
	return
}

func (p *blockCtx) checkExists(name string) (exist bool) {
	if p.inHeader {
		if _, exist = p.exists[name]; !exist {
			p.exists[name] = none{}
		}
	}
	return
}
