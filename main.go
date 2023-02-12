package main

import (
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := Main(ctx, flag.Args())
	if err != nil {
		log.Fatal(err)
	}
}

func Main(ctx context.Context, args []string) error {
	ps, err := GetPackages(ctx, args)
	if err != nil {
		return err
	}

	total := New("<total>")
	counts := []*Count{}
	for _, p := range ps {
		c := CountPackage(p)
		total.Add(c)
		counts = append(counts, c)
	}

	sort.Slice(counts, func(i, j int) bool {
		return counts[i].ID < counts[j].ID
	})
	// don't include total if there's only one package
	if len(counts) > 1 {
		counts = append(counts, total)
	}

	for _, c := range counts {
		fmt.Println(c)
	}
	return nil
}

func GetPackages(ctx context.Context, pattern []string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		// does not count as RHS is expression
		Mode: packages.NeedTypesInfo | packages.NeedTypes | packages.NeedSyntax | packages.NeedFiles,
		// counts as simple ident but neither exact nor partial match
		Context: ctx,
	}
	ps, err := packages.Load(cfg, pattern...)
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(ps) > 0 {
		return nil, fmt.Errorf("could not load packages")
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("no packages to load")
	}
	return ps, nil
}

func CountPackage(p *packages.Package) *Count {
	count := New(p.ID)
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CompositeLit); ok {
				// only care if composite lit of a struct type
				typ := p.TypesInfo.Types[c].Type
				if _, ok := typ.Underlying().(*types.Struct); ok {
					keyed := false
					for _, x := range c.Elts {
						// only care if keyed
						if kv, ok := x.(*ast.KeyValueExpr); ok {
							keyed = true
							count.Count(MatchOf(kv))
						}
					}
					if keyed {
						count.Literals++
					}
				}
			}
			return true
		})
	}
	return count
}

type Match struct {
	Identical, Partial           bool
	Regular, Star, Amp, Selector bool
}

func MatchOf(kv *ast.KeyValueExpr) *Match {
	var Star, Amp bool
	ident, Selector := GetIdentFrom(kv.Value)

	// if these fire ident was nil anyway
	switch v := kv.Value.(type) {
	case *ast.StarExpr:
		// only count *name
		ident, Selector = GetIdentFrom(v.X)
		Star = true
	case *ast.UnaryExpr:
		// only count &name
		if v.Op == token.AND {
			ident, Selector = GetIdentFrom(v.X)
			Amp = true
		}
	}
	if ident == nil {
		return nil
	}

	key := kv.Key.(*ast.Ident).Name
	name := ident.Name

	Identical := key == name
	// only count partial matches when not identical and for name not name.name
	partial := !Identical && !Selector && strings.EqualFold(key, name)

	return &Match{
		Regular: !Star && !Amp && !Selector,
		// Partial is a partial match so we have one for testing
		Partial: partial,
		// These all count as simple idents with exact matches
		Identical: Identical,
		Star:      Star,
		Amp:       Amp,
		Selector:  Selector,
	}
}

func GetIdentFrom(n ast.Node) (ident *ast.Ident, selector bool) {
	switch v := n.(type) {
	case *ast.Ident:
		ident = v
	case *ast.SelectorExpr:
		// only count name.name
		if _, ok := v.X.(*ast.Ident); ok {
			ident, selector = v.Sel, true
		}
	}
	return ident, selector
}

type Count struct {
	ID                                                            string
	Literals, KV, NotIdent                                        uint64
	Ident, QualifiedIdent, Star, QualifiedStar, Amp, QualifiedAmp *Tally
}

func New(ID string) *Count {
	return &Count{
		// simple ident with exact match
		ID: ID,
		// arbitrary expressions
		Ident:          &Tally{},
		QualifiedIdent: &Tally{},
		Star:           &Tally{},
		QualifiedStar:  &Tally{},
		Amp:            &Tally{},
		QualifiedAmp:   &Tally{},
	}
}

func (c *Count) Count(m *Match) {
	// inc total KV pairs
	c.KV++
	if m == nil {
		// inc count of expressions
		c.NotIdent++
		return
	}
	// figure out which tally to update
	var t *Tally
	switch {
	case m.Regular:
		t = c.Ident
	case m.Star:
		if m.Selector {
			t = c.QualifiedStar
		} else {
			t = c.Star
		}
	case m.Amp:
		if m.Selector {
			t = c.QualifiedAmp
		} else {
			t = c.Amp
		}
	case m.Selector:
		t = c.QualifiedIdent
	}
	t.Count(m.Identical, m.Partial)
}

func (c *Count) Add(o *Count) {
	c.Literals += o.Literals
	c.KV += o.KV
	c.NotIdent += o.NotIdent
	c.Ident.Add(o.Ident)
	c.QualifiedIdent.Add(o.QualifiedIdent)
	c.Star.Add(o.Star)
	c.QualifiedStar.Add(o.QualifiedStar)
	c.Amp.Add(o.Amp)
	c.QualifiedAmp.Add(o.QualifiedAmp)
}

func (c *Count) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: ", c.ID)
	if c.Literals == 0 {
		b.WriteString("no keyed struct literals\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n\tkeyed struct literals: %d\n", c.Literals)
	fmt.Fprintf(&b, "\ttotal KV pairs: %d\n\tnon-candidate KV pairs: %d\n", c.KV, c.NotIdent)
	tally := func(nm string, qual bool, t *Tally) {
		if t.Total == 0 {
			return
		}
		fmt.Fprintf(&b, "\t%s:\n", nm)
		fmt.Fprintf(&b, "\t\ttotal: %d\n", t.Total)
		fmt.Fprintf(&b, "\t\tno match: %d\n", t.Total-t.Exact-t.EqualsFold)
		fmt.Fprintf(&b, "\t\texact: %d\n", t.Exact)
		fmt.Fprintf(&b, "\t\tpartial: ")
		if qual {
			fmt.Fprintf(&b, "N/A\n")
		} else {
			fmt.Fprintf(&b, "%d\n", t.EqualsFold)
		}
	}
	tally("ident", false, c.Ident)
	tally("qual.ident", true, c.QualifiedIdent)
	tally("*ident", false, c.Star)
	tally("*qual.ident", true, c.QualifiedStar)
	tally("&ident", false, c.Amp)
	tally("&qual.ident", true, c.QualifiedAmp)
	return b.String()
}

type Tally struct {
	Total, Exact, EqualsFold uint64
}

func (t *Tally) Count(Exact, EqualsFold bool) {
	t.Total++
	if Exact {
		t.Exact++
	} else if EqualsFold {
		t.EqualsFold++
	}
}

func (t *Tally) Add(o *Tally) {
	t.Total += o.Total
	t.Exact += o.Exact
	t.EqualsFold += o.EqualsFold
}
