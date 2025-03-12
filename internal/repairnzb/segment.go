package repairnzb

import "github.com/Tensai75/nzbparser"

type segment struct {
	nzbparser.NzbSegment
	groups []string
}
