package indexer

import (
	"log"
	"sync"
	"time"

	"github.com/codetrek/haystack/internal/core/workspace"
	"github.com/codetrek/haystack/searchcore/documents"
)

type WriteDoc struct {
	Workspace *workspace.Workspace
	Document  *documents.Document
	CreateNew bool
}

type Writer struct {
	docs chan *WriteDoc
	stop chan struct{}
	done chan struct{}
}

func NewWriter() *Writer {
	return &Writer{
		docs: make(chan *WriteDoc, 64),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

func (w *Writer) Start(wg *sync.WaitGroup) {
	wg.Add(1)
	go w.run(wg)
}

func (w *Writer) Stop() {
	close(w.stop)
	<-w.done
	log.Println("[Indexer] Writer stopped")
}

func (w *Writer) run(wg *sync.WaitGroup) {
	log.Println("[Indexer] Writer started")
	defer wg.Done()

	for {
		select {
		case doc := <-w.docs:
			docs := []*WriteDoc{doc}
			docs = append(docs, w.getPendingWrites(7)...)

			w.processDocs(docs)
		case <-w.stop:
			for {
				docs := w.getPendingWrites(8)
				if len(docs) == 0 {
					break
				}
				w.processDocs(docs)

				// Sleep to wait for remaining docs to be added to the channel
				time.Sleep(100 * time.Millisecond)
			}
			close(w.done)
			return
		}
	}
}

func (w *Writer) processDocs(docs []*WriteDoc) {
	mu.Lock()
	st := stInst
	mu.Unlock()

	newDocs := make(map[int][]*documents.Document)
	existingDocs := make(map[int][]*documents.Document)

	for _, doc := range docs {
		if doc.Workspace.IsDeleted() {
			delete(newDocs, doc.Workspace.Id)
			delete(existingDocs, doc.Workspace.Id)
			continue
		}

		if doc.CreateNew {
			newDocs[doc.Workspace.Id] = append(newDocs[doc.Workspace.Id], doc.Document)
		} else {
			existingDocs[doc.Workspace.Id] = append(existingDocs[doc.Workspace.Id], doc.Document)
		}
	}

	for workspaceID, docs := range newDocs {
		st.SaveNewDocuments(workspaceID, docs)
	}

	for workspaceID, docs := range existingDocs {
		st.UpdateDocuments(workspaceID, docs)
	}
}

func (w *Writer) getPendingWrites(limit int) []*WriteDoc {
	docs := []*WriteDoc{}
	for {
		select {
		case doc := <-w.docs:
			docs = append(docs, doc)
			if len(docs) >= limit {
				return docs
			}
		default:
			return docs
		}
	}
}

func (w *Writer) Add(workspace *workspace.Workspace, doc *documents.Document, createNew bool) {
	if workspace.IsDeleted() {
		return
	}

	w.docs <- &WriteDoc{
		Workspace: workspace,
		Document:  doc,
		CreateNew: createNew,
	}
}
