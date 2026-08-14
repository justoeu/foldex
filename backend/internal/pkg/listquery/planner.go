package listquery

import (
	"fmt"
	"strings"

	"foldex/internal/pkg/authctx"
)

type entityKind string

const (
	linkKind entityKind = "link"
	noteKind entityKind = "note"
)

type Entity struct {
	alias          string
	kind           entityKind
	search         []string
	unlockedFolder string
}

type OrderColumns struct {
	pinned        string
	createdAt     string
	clickCount    string
	lastClickedAt string
	title         string
}

func LinkEntity(unlockedFolder string) Entity {
	const alias = "l"
	return Entity{
		alias: alias, kind: linkKind,
		search:         []string{alias + ".title", alias + ".url", "COALESCE(" + alias + ".description,'')"},
		unlockedFolder: unlockedFolder,
	}
}

func NoteEntity(unlockedFolder string) Entity {
	const alias = "n"
	return Entity{
		alias: alias, kind: noteKind,
		search:         []string{alias + ".title", alias + ".body_text"},
		unlockedFolder: unlockedFolder,
	}
}

func LinkOrder() OrderColumns {
	return tableOrder("l")
}

func NoteOrder() OrderColumns {
	return tableOrder("n")
}

func tableOrder(entityAlias string) OrderColumns {
	const clickAlias = "cl"
	return OrderColumns{
		pinned: entityAlias + ".pinned", createdAt: entityAlias + ".created_at",
		clickCount: "COALESCE(" + clickAlias + ".cnt, 0)", lastClickedAt: clickAlias + ".last_at",
		title: entityAlias + ".title",
	}
}

func UnionOrder() OrderColumns {
	return OrderColumns{
		pinned: "pinned", createdAt: "created_at", clickCount: "click_count",
		lastClickedAt: "last_clicked_at", title: "title",
	}
}

type Scope struct {
	Where    []string
	OwnerArg int
}

type Page struct {
	OrderBy   string
	LimitArg  int
	OffsetArg int
}

// Planner owns the common list contract while allowing entries to append two
// independently owner-scoped UNION arms to one argument list.
type Planner struct {
	params Params
	args   []any
}

func NewPlanner(params Params) *Planner {
	return &Planner{params: params}
}

func (p *Planner) AddScope(uid authctx.UserID, entity Entity) Scope {
	p.args = append(p.args, int64(uid))
	ownerArg := len(p.args)
	where := []string{fmt.Sprintf("%s.user_id = $%d", entity.alias, ownerArg)}

	if p.params.Q != "" {
		p.args = append(p.args, "%"+p.params.Q+"%")
		arg := len(p.args)
		search := make([]string, 0, len(entity.search))
		for _, expression := range entity.search {
			search = append(search, fmt.Sprintf("%s ILIKE $%d", expression, arg))
		}
		where = append(where, "("+strings.Join(search, " OR ")+")")
	}
	if len(p.params.TagIDs) > 0 {
		p.args = append(p.args, p.params.TagIDs)
		arg := len(p.args)
		where = append(where, fmt.Sprintf(`%s.id IN (
            SELECT entity_id FROM link_tag
            WHERE entity_kind = '%s' AND tag_id = ANY($%d)
            GROUP BY entity_id
			HAVING count(DISTINCT tag_id) = %d
		)`, entity.alias, entity.kind, arg, len(p.params.TagIDs)))
	}
	if p.params.FolderID != nil {
		p.args = append(p.args, *p.params.FolderID)
		where = append(where, fmt.Sprintf("%s.folder_id = $%d", entity.alias, len(p.args)))
	} else if p.params.Ungrouped {
		where = append(where, entity.alias+".folder_id IS NULL")
	} else if entity.unlockedFolder != "" {
		where = append(where, entity.unlockedFolder)
	}

	return Scope{Where: where, OwnerArg: ownerArg}
}

func (p *Planner) AddPage(columns OrderColumns) Page {
	order := columns.pinned + " DESC, " + columns.createdAt + " DESC"
	switch p.params.Sort {
	case "clicks":
		order = columns.pinned + " DESC, " + columns.clickCount + " DESC, " + columns.createdAt + " DESC"
	case "recent":
		order = columns.pinned + " DESC, COALESCE(" + columns.lastClickedAt + ", " + columns.createdAt + ") DESC"
	case "alpha":
		order = columns.pinned + " DESC, lower(" + columns.title + ") ASC, " + columns.createdAt + " DESC"
	case "alpha_desc":
		order = columns.pinned + " DESC, lower(" + columns.title + ") DESC, " + columns.createdAt + " DESC"
	}

	limit := p.params.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := p.params.Offset
	if offset < 0 {
		offset = 0
	}
	p.args = append(p.args, limit, offset)
	return Page{OrderBy: order, LimitArg: len(p.args) - 1, OffsetArg: len(p.args)}
}

func (p *Planner) Args() []any {
	return append([]any(nil), p.args...)
}
