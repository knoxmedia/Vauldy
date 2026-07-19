package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestMusicAndTVPlayGETsDoNotUseContextlessDBQueries(t *testing.T) {
	fset := token.NewFileSet()
	files := []string{"music.go", "music_artwork.go", "series.go"}
	targets := map[string]bool{"ListLibraryAlbums": true, "ListLibraryArtists": true, "ListLibraryGenres": true, "ListLibraryTracks": true, "GetAlbum": true, "GetAlbumPlayTarget": true, "ServeAlbumArtwork": true, "ListArtistAlbums": true, "ListGenreAlbums": true, "GetArtist": true, "ServeArtistArtwork": true, "GetSeriesPlayTarget": true}
	for _, name := range files {
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !targets[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (sel.Sel.Name == "Query" || sel.Sel.Name == "QueryRow") {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "c" {
						return true
					}
					t.Errorf("%s uses contextless %s at %s", fn.Name.Name, sel.Sel.Name, fset.Position(sel.Pos()))
				}
				return true
			})
		}
	}
}
