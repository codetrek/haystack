import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { fileURLToPath, pathToFileURL  } from 'url';
import path from 'path';
import fs from 'fs/promises';
import { loadDatasetFromFile, DatasetItem } from "../index.js";
import { z } from "zod";

const __filename = fileURLToPath(import.meta.url);

// Array of random search words
let searchWords: string[] = [];
let dataset: DatasetItem[];

export async function createServer(): Promise<McpServer> {
  const server = new McpServer({
    name: "Weather MCP Server",
    version: "0.1.0",
  });

  const filePath = path.join(__filename, '../../../dataset.json');
  
  dataset = await loadDatasetFromFile(filePath);
  for (const item of dataset) {
    if (item.summary && typeof item.summary === 'string') {
      searchWords.push(`#codebase where is the code "${item.summary}"`);
    }
  }

  // server.tool(
  //   "query_task",
  //   "Get a task for querying a specific function.",
  //   async () => {
  //     let word = "";      
  //     if (searchWords.length > 0) {
  //       word = searchWords[searchWords.length - 1];
  //       searchWords.pop();
  //     }

  //     return {
  //       content: [
  //         {
  //           type: "text",
  //           text: word,
  //         },
  //       ],
  //     };
  //   }
  // );
  server.tool(
    "save_search_result",
    "Save the search result",
    {
      task: z.string().describe("The task that user input. User's original input, **DONT** change anything."),
      file_path: z.string().describe("The file path of the search result(where this symbol is founded)."),
      line_number: z.number().describe("The line number of the search result."),
      result: z.string().describe("A conclusion summarized in no more than three sentences."),
    },
    async (params) => {
      try {
        // Create a log entry with timestamp, words, and result
        const timestamp = new Date().toISOString();
        const logEntry = {
          timestamp,
          task: params.task,
          filePath: params.file_path,
          lineNumber: params.line_number,
          result: params.result
        };
        
        const resultsPath = path.join(path.dirname(__filename), '../../search_results.json');
        
        let existingData = [];
        try {
          const fileData = await fs.readFile(resultsPath, 'utf-8');
          existingData = JSON.parse(fileData);
          if (!Array.isArray(existingData)) {
            existingData = [];
          }
        } catch (err) {
          // File doesn't exist or isn't valid JSON, start with empty array
        }
        
        // Add new entry to data
        existingData.push(logEntry);
        
        // Write back to file
        await fs.writeFile(resultsPath, JSON.stringify(existingData, null, 2), 'utf-8');
        
        return {
          type: "tool_response",
          content: [
            {
              type: "text",
              text: "✅ Task result saved. You can continue.",
            },
          ],
        };      
      } catch (error: any) {
        return {
          content: [
            {
              type: "text",
              text: `Error: ${error.message || 'Unknown error'}`,
            },
          ],
        };
      }
    }
  );
  
  return server;
}


