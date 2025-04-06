package repairnzb

import "github.com/Tensai75/nzbparser"

type brokenSegment struct {
	segment *nzbparser.NzbSegment
	file    *nzbparser.NzbFile
}
