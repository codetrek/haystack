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

type PromptParser struct {
	prompts     chan *PromptData
	stop        chan struct{}
	done        chan struct{}
	initialized bool
}

func NewPromptParser() *PromptParser {
	return &PromptParser{
		prompts: make(chan *PromptData, 64),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (ps *PromptParser) Start(wg *sync.WaitGroup) {
	if !conf.Get().Symbols.EnvInstalled || !conf.Get().Symbols.EnablePromptSearch {
		log.Println("[Indexer] PromptParser did not start: Environment not installed or feature disabled")
		return
	}

	ps.initialized = true
	wg.Add(1)
	go ps.run(wg)
}

func (ps *PromptParser) Stop() {
	if !ps.initialized {
		log.Println("[Indexer] PromptParser was not started, nothing to stop")
		return
	}

	close(ps.stop)
	<-ps.done
	log.Println("[Indexer] PromptParser stopped")
}

func (ps *PromptParser) run(wg *sync.WaitGroup) {
	log.Println("[Indexer] PromptParser started, waiting for EmbeddingEngine to initialize...")
	defer wg.Done()

	// Wait for embedding engine to initialize
	initResult := <-embeddingEngine.initialized
	if initResult.Success {
		log.Println("[Indexer] PromptParser: EmbeddingEngine initialized successfully, starting prompt processing...")
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

func (ps *PromptParser) processPromptData(promptData *PromptData) {
	if promptData.Workspace.IsDeleted() || len(promptData.PromptPaths) == 0 {
		return
	}

	prompts.SavePrompts(promptData.Workspace, promptData.PromptPaths)
}

func (ps *PromptParser) Add(workspace *workspace.Workspace, promptPaths []string) {
	if workspace.IsDeleted() || len(promptPaths) == 0 {
		return
	}

	ps.prompts <- &PromptData{
		Workspace:   workspace,
		PromptPaths: promptPaths,
	}
}
