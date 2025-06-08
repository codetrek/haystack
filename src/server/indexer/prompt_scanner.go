package indexer

import (
	"log"
	"sync"

	"github.com/ai-microsoft/haystack/conf"
	"github.com/ai-microsoft/haystack/server/core/prompts"
	"github.com/ai-microsoft/haystack/server/core/workspace"
)

type PromptData struct {
	Workspace   *workspace.Workspace
	PromptPaths []string
}

type PromptScanner struct {
	prompts     chan *PromptData
	stop        chan struct{}
	done        chan struct{}
	initialized bool
}

func NewPromptScanner() *PromptScanner {
	return &PromptScanner{
		prompts: make(chan *PromptData, 64),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (ps *PromptScanner) Start(wg *sync.WaitGroup) {
	if !conf.Get().Symbols.EnvInstalled || !conf.Get().Symbols.EnablePromptSearch {
		log.Println("[Indexer] PromptScanner did not start: Environment not installed or feature disabled")
		return
	}

	ps.initialized = true
	wg.Add(1)
	go ps.run(wg)
}

func (ps *PromptScanner) Stop() {
	if !ps.initialized {
		log.Println("[Indexer] PromptScanner was not started, nothing to stop")
		return
	}

	close(ps.stop)
	<-ps.done
	log.Println("[Indexer] PromptScanner stopped")
}

func (ps *PromptScanner) run(wg *sync.WaitGroup) {
	log.Println("[Indexer] PromptScanner started, waiting for EmbeddingEngine to initialize...")
	defer wg.Done()

	// Wait for embedding engine to initialize
	initResult := <-embeddingEngine.initialized
	if initResult.Success {
		log.Println("[Indexer] PromptScanner: EmbeddingEngine initialized successfully, starting prompt processing...")
		for {
			select {
			case promptData := <-ps.prompts:
				ps.processPromptData(promptData)
			case <-ps.stop:
				// Process remaining prompts before stopping
				for {
					select {
					case promptData := <-ps.prompts:
						ps.processPromptData(promptData)
					default:
						close(ps.done)
						return
					}
				}
			}
		}
	}

}

func (ps *PromptScanner) processPromptData(promptData *PromptData) {
	if promptData.Workspace.IsDeleted() || len(promptData.PromptPaths) == 0 {
		return
	}

	prompts.SavePrompts(promptData.Workspace, promptData.PromptPaths)
}

func (ps *PromptScanner) Add(workspace *workspace.Workspace, promptPaths []string) {
	if workspace.IsDeleted() || len(promptPaths) == 0 {
		return
	}

	ps.prompts <- &PromptData{
		Workspace:   workspace,
		PromptPaths: promptPaths,
	}
}
