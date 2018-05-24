package sharing

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/instance"
	"github.com/cozy/cozy-stack/pkg/lock"
	"github.com/cozy/cozy-stack/pkg/vfs"
)

// TrackMessage is used for jobs on the share-track worker.
// It's the same for all the jobs of a trigger.
type TrackMessage struct {
	SharingID string `json:"sharing_id"`
	RuleIndex int    `json:"rule_index"`
	DocType   string `json:"doctype"`
}

// TrackEvent is used for jobs on the share-track worker.
// It's unique per job.
type TrackEvent struct {
	Verb   string           `json:"verb"`
	Doc    couchdb.JSONDoc  `json:"doc"`
	OldDoc *couchdb.JSONDoc `json:"old,omitempty"`
}

// SharedInfo gives informations about how to apply the sharing to the shared
// document
type SharedInfo struct {
	// Rule is the index of the rule inside the sharing rules
	Rule int `json:"rule"`

	// Removed is true for a deleted document, a trashed file, or if the
	// document does no longer match the sharing rule
	Removed bool `json:"removed,omitempty"`

	// Binary is a boolean flag that is true only for files (and not even
	// folders) with `removed: false`
	Binary bool `json:"binary,omitempty"`
}

// RevsTree is a tree of revisions, like CouchDB has.
// The revisions are sorted by growing generation (the number before the hyphen).
// http://docs.couchdb.org/en/2.1.1/replication/conflicts.html#revision-tree
type RevsTree struct {
	// Rev is a revision, with the generation and the id
	// e.g. 1-1bad9a88f0a608ea78c12ab49882ac41
	Rev string `json:"rev"`

	// Branches is the list of revisions that have this revision for parent.
	// The general case is to have only one branch, but we can have more with
	// conflicts.
	Branches []RevsTree `json:"branches"`
}

// Clone duplicates the RevsTree
func (rt *RevsTree) Clone() RevsTree {
	cloned := RevsTree{Rev: rt.Rev}
	cloned.Branches = make([]RevsTree, len(rt.Branches))
	for i, b := range rt.Branches {
		cloned.Branches[i] = b.Clone()
	}
	return cloned
}

// Generation returns the maximal generation of a revision in this tree
func (rt *RevsTree) Generation() int {
	if len(rt.Branches) == 0 {
		return RevGeneration(rt.Rev)
	}
	max := 0
	for _, b := range rt.Branches {
		if g := b.Generation(); g > max {
			max = g
		}
	}
	return max
}

// Find returns the sub-tree for the given revision, or nil if not found.
func (rt *RevsTree) Find(rev string) *RevsTree {
	if rt.Rev == rev {
		return rt
	}
	for i := range rt.Branches {
		if sub := rt.Branches[i].Find(rev); sub != nil {
			return sub
		}
	}
	return nil
}

// Add inserts the given revision in the main branch
func (rt *RevsTree) Add(rev string) *RevsTree {
	// TODO check generations (conflicts)
	if len(rt.Branches) > 0 {
		return rt.Branches[0].Add(rev)
	}
	rt.Branches = []RevsTree{
		{Rev: rev},
	}
	return &rt.Branches[0]
}

// InsertAfter inserts the given revision in the tree as a child of the second
// revision.
func (rt *RevsTree) InsertAfter(rev, parent string) {
	subtree := rt.Find(parent)
	if subtree == nil {
		subtree = rt.Add(parent)
	}
	for _, b := range subtree.Branches {
		if b.Rev == rev {
			return
		}
	}
	subtree.Branches = append(subtree.Branches, RevsTree{Rev: rev})
	// TODO rebalance (conflicts)
}

// InsertChain inserts a chain of revisions, ie the first revision is the
// parent of the second revision, which is itself the parent of the third
// revision, etc. The first revisions of the chain are very probably already in
// the tree, the last one is certainly not.
func (rt *RevsTree) InsertChain(chain []string) {
	if len(chain) == 0 {
		return
	}
	subtree := rt.Find(chain[0])
	if subtree == nil {
		subtree = rt.Add(chain[0])
	}
	for _, rev := range chain[1:] {
		if len(subtree.Branches) > 0 {
			found := false
			for i := range subtree.Branches {
				if subtree.Branches[i].Rev == rev {
					found = true
					subtree = &subtree.Branches[i]
					break
				}
			}
			if found {
				continue
			}
		}
		subtree.Branches = append(subtree.Branches, RevsTree{Rev: rev})
		subtree = &subtree.Branches[0]
	}
	// TODO rebalance (conflicts)
}

// SharedRef is the struct for the documents in io.cozy.shared.
// They are used to track which documents is in which sharings.
type SharedRef struct {
	// SID is the identifier, it is doctype + / + id of the referenced doc
	SID  string `json:"_id,omitempty"`
	SRev string `json:"_rev,omitempty"`

	// Revisions is a tree with the last known _rev of the shared object.
	Revisions *RevsTree `json:"revisions"`

	// Infos is a map of sharing ids -> informations
	Infos map[string]SharedInfo `json:"infos"`
}

// ID returns the sharing qualified identifier
func (s *SharedRef) ID() string { return s.SID }

// Rev returns the sharing revision
func (s *SharedRef) Rev() string { return s.SRev }

// DocType returns the sharing document type
func (s *SharedRef) DocType() string { return consts.Shared }

// SetID changes the sharing qualified identifier
func (s *SharedRef) SetID(id string) { s.SID = id }

// SetRev changes the sharing revision
func (s *SharedRef) SetRev(rev string) { s.SRev = rev }

// Clone implements couchdb.Doc
func (s *SharedRef) Clone() couchdb.Doc {
	cloned := *s
	revs := s.Revisions.Clone()
	cloned.Revisions = &revs
	cloned.Infos = make(map[string]SharedInfo, len(s.Infos))
	for k, v := range s.Infos {
		cloned.Infos[k] = v
	}
	return &cloned
}

// Match implements the permissions.Matcher interface
func (s *SharedRef) Match(key, value string) bool {
	switch key {
	case "sharing":
		_, ok := s.Infos[value]
		return ok
	}
	return false
}

// RevGeneration returns the number before the hyphen, called the generation of a revision
func RevGeneration(rev string) int {
	parts := strings.SplitN(rev, "-", 2)
	gen, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return gen
}

// FindReferences returns the io.cozy.shared references to the given identifiers
func FindReferences(inst *instance.Instance, ids []string) ([]*SharedRef, error) {
	var refs []*SharedRef
	req := &couchdb.AllDocsRequest{Keys: ids}
	if err := couchdb.GetAllDocs(inst, consts.Shared, req, &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

// extractReferencedBy extracts the referenced_by slice from the given doc
// and cast it to the right type
func extractReferencedBy(doc *couchdb.JSONDoc) []couchdb.DocReference {
	slice, _ := doc.Get(couchdb.SelectorReferencedBy).([]interface{})
	refs := make([]couchdb.DocReference, len(slice))
	for i, ref := range slice {
		refs[i], _ = ref.(couchdb.DocReference)
	}
	return refs
}

// isNoLongerShared returns true for a document/file/folder that has matched a
// rule of a sharing, but no longer does.
func isNoLongerShared(inst *instance.Instance, msg TrackMessage, evt TrackEvent) (bool, error) {
	if msg.DocType != consts.Files {
		return false, nil // TODO rules for documents with a selector
	}

	// Optim: if dir_id and referenced_by have not changed, the file/folder
	// can't have been removed from the sharing. Same if it has no old doc.
	if evt.OldDoc == nil {
		return false, nil
	}
	if evt.OldDoc.Get("dir_id") == evt.Doc.Get("dir_id") {
		olds := extractReferencedBy(evt.OldDoc)
		news := extractReferencedBy(&evt.Doc)
		if vfs.SameReferences(olds, news) {
			return false, nil
		}
	}

	s, err := FindSharing(inst, msg.SharingID)
	if err != nil {
		return false, err
	}
	rule := s.Rules[msg.RuleIndex]
	if rule.Selector == couchdb.SelectorReferencedBy {
		refs := extractReferencedBy(&evt.Doc)
		for _, ref := range refs {
			if rule.hasReferencedBy(ref) {
				return false, nil
			}
		}
		return true, nil
	}

	var docPath string
	if evt.Doc.Get("type") == consts.FileType {
		dirID, ok := evt.Doc.Get("dir_id").(string)
		if !ok {
			return false, ErrInternalServerError
		}
		var parent *vfs.DirDoc
		parent, err = inst.VFS().DirByID(dirID)
		if err != nil {
			return false, err
		}
		docPath = parent.Fullpath
	} else {
		p, ok := evt.Doc.Get("path").(string)
		if !ok {
			return false, ErrInternalServerError
		}
		docPath = p
	}
	sharingDir, err := inst.VFS().DirByID(rule.Values[0])
	if err != nil {
		return false, err
	}
	return !strings.HasPrefix(docPath+"/", sharingDir.Fullpath+"/"), nil
}

// UpdateShared updates the io.cozy.shared database when a document is
// created/update/removed
func UpdateShared(inst *instance.Instance, msg TrackMessage, evt TrackEvent) error {
	mu := lock.ReadWrite(inst.Domain + "/shared")
	mu.Lock()
	defer mu.Unlock()

	evt.Doc.Type = msg.DocType
	sid := evt.Doc.Type + "/" + evt.Doc.ID()
	var ref SharedRef
	if err := couchdb.GetDoc(inst, consts.Shared, sid, &ref); err != nil {
		if !couchdb.IsNotFoundError(err) {
			return err
		}
		ref.SID = sid
		ref.Infos = make(map[string]SharedInfo)
	}

	if _, ok := ref.Infos[msg.SharingID]; !ok {
		ref.Infos[msg.SharingID] = SharedInfo{
			Rule:   msg.RuleIndex,
			Binary: evt.Doc.Type == consts.Files && evt.Doc.Get("type") == consts.FileType,
		}
	}

	if evt.Verb == "DELETED" || isTrashed(evt.Doc) {
		ref.Infos[msg.SharingID] = SharedInfo{
			Rule:    ref.Infos[msg.SharingID].Rule,
			Removed: true,
			Binary:  false,
		}
	} else {
		removed, err := isNoLongerShared(inst, msg, evt)
		if err != nil {
			return err
		}
		if removed {
			ref.Infos[msg.SharingID] = SharedInfo{
				Rule:    ref.Infos[msg.SharingID].Rule,
				Removed: true,
				Binary:  false,
			}
			// TODO for a folder, we should also mark files inside it as removed
		}
	}

	rev := evt.Doc.Rev()
	if ref.Rev() == "" {
		ref.Revisions = &RevsTree{Rev: rev}
		return couchdb.CreateNamedDoc(inst, &ref)
	}
	var oldrev string
	if evt.OldDoc != nil {
		oldrev = evt.OldDoc.Rev()
	}
	ref.Revisions.InsertAfter(rev, oldrev)
	return couchdb.UpdateDoc(inst, &ref)
}

// RemoveSharedRefs deletes the references containing the sharingid
func RemoveSharedRefs(inst *instance.Instance, sharingID string) error {
	var req = &couchdb.ViewRequest{
		Key:         sharingID,
		IncludeDocs: true,
	}
	var res couchdb.ViewResponse
	err := couchdb.ExecView(inst, consts.SharedDocsBySharingID, req, &res)
	if err != nil {
		return err
	}

	for _, row := range res.Rows {
		var doc SharedRef
		if err = json.Unmarshal(row.Doc, &doc); err != nil {
			return err
		}
		// Remove the ref if there are others sharings; remove the doc otherwise
		if len(doc.Infos) > 1 {
			delete(doc.Infos, sharingID)
			if err = couchdb.UpdateDoc(inst, &doc); err != nil {
				return err
			}
		} else {
			if err = couchdb.DeleteDoc(inst, &doc); err != nil {
				return err
			}
		}
	}
	return nil
}

// GetSharedDocsBySharingIDs returns a map associating each given sharingID
// to a list of DocReference, which are the shared documents
func GetSharedDocsBySharingIDs(inst *instance.Instance, sharingIDs []string) (map[string][]couchdb.DocReference, error) {
	keys := make([]interface{}, len(sharingIDs))
	for i, id := range sharingIDs {
		keys[i] = id
	}
	var req = &couchdb.ViewRequest{
		Keys:        keys,
		IncludeDocs: true,
	}
	var res couchdb.ViewResponse

	err := couchdb.ExecView(inst, consts.SharedDocsBySharingID, req, &res)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]couchdb.DocReference, len(res.Rows))

	for _, row := range res.Rows {
		var doc SharedRef
		err := json.Unmarshal(row.Doc, &doc)
		if err != nil {
			return nil, err
		}
		sID := row.Key.(string)
		// Filter out the removed docs
		if !doc.Infos[sID].Removed {
			docRef := extractDocReferenceFromID(doc.ID())
			result[sID] = append(result[sID], *docRef)
		}
	}
	return result, nil
}

// GetSharingsByDocType returns all the sharings for the given doctype
func GetSharingsByDocType(inst *instance.Instance, docType string) (map[string]*Sharing, error) {
	var req = &couchdb.ViewRequest{
		Key:         docType,
		IncludeDocs: true,
	}
	var res couchdb.ViewResponse
	err := couchdb.ExecView(inst, consts.SharingsByDocTypeView, req, &res)
	if err != nil {
		return nil, err
	}
	sharings := make(map[string]*Sharing, len(res.Rows))

	for _, row := range res.Rows {
		var doc Sharing
		err := json.Unmarshal(row.Doc, &doc)
		if err != nil {
			return nil, err
		}
		// Avoid duplicates, i.e. a set a rules having the same doctype
		sID := row.Value.(string)
		if _, ok := sharings[sID]; !ok {
			sharings[sID] = &doc
		}
	}
	return sharings, nil
}

// extractDocReferenceFromID takes a string formatted as doctype/docid and
// returns a DocReference
func extractDocReferenceFromID(id string) *couchdb.DocReference {
	var ref couchdb.DocReference
	slice := strings.SplitN(id, "/", 2)
	if len(slice) != 2 {
		return nil
	}
	ref.ID = slice[1]
	ref.Type = slice[0]
	return &ref
}

var _ couchdb.Doc = &SharedRef{}
