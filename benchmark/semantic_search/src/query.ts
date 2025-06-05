import path from 'path';
import { getConfig } from './conf.js';

interface SymbolFile {
  path: string;
  line: number;
}

interface Symbol {
  name: string;
  files: SymbolFile[];
}

interface SymbolSearchResponse {
  code: number;
  message: string;
  data: {
    query: string;
    symbols: Symbol[];
  };
}

/**
 * Query the Haystack server for symbols matching the given query
 * @param query The symbol name to search for
 * @param limit Maximum number of results to return
 * @param maxPerFile Maximum number of results to return per file
 * @param workspace The workspace path to search in
 * @returns A promise that resolves to the search results
 */
export async function queryFromHaystack(query: string, limit: number, maxPerFile: number, workspace: string = "D:\\Edge\\src\\chrome\\browser\\"): Promise<SymbolSearchResponse | null> {
  try {
    const url = getConfig().haystackSymbolUrl;
    
    const requestBody = {
      query,
      limit: {
        max_results: limit,
        max_results_per_file: maxPerFile
      },
      workspace
    };
    
    const response = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json'
      },
      body: JSON.stringify(requestBody)
    });
    
    if (!response.ok) {
      console.error(`Error searching symbols: ${response.status} ${response.statusText}`);
      return null;
    }
    return await response.json() as SymbolSearchResponse;
  } catch (error) {
    console.error('Error querying Haystack:', error);
    return null;
  }
}

export async function matchFromHaystack(query: string, limit: number, maxPerFile: number, workspace: string, dstFile: string, dstLine: number): Promise<[number, number, number]> {
  try {
    // Query Haystack for symbol results
    const response = await queryFromHaystack(query, limit, maxPerFile, workspace);
    if (!response) {
      console.error('No response from Haystack');
      return [-1, -1, -1];
    }
    
    // Normalize the destination file path to handle different slash types
    const normalizedDstFile = dstFile.replace(/\\/g, '/');
    if (!response.data.symbols) {
      return [-1, -1, -1];
    }
    
    let exactPathMatch = -1;  // Position of first match with exact path
    let lineProximityMatch = -1;  // Position of first match with exact path and line proximity
    
    // Check each symbol and its files for a match
    for (let i = 0; i < response.data.symbols.length; i++) {
      const symbol = response.data.symbols[i];
      
      // Check each file associated with this symbol
      for (const file of symbol.files) {
        const fullPath = path.join(workspace, file.path).replace(/\\/g, '/');
        
        // Match condition 1: Exact file path match
        if (normalizedDstFile === fullPath && exactPathMatch === -1) {
          exactPathMatch = i;
        }
        
        // Match condition 2: File path match and line proximity
        if (normalizedDstFile === fullPath && 
            Math.abs(file.line - dstLine) <= 5 && lineProximityMatch === -1) {
          lineProximityMatch = i;
          break;
        }
      }
      
      // If we've found both types of matches, we can exit early
      if (exactPathMatch !== -1 && lineProximityMatch !== -1) {
        break;
      }
    }
    
    return [exactPathMatch, lineProximityMatch, response.data.symbols.length];
  } catch (error) {
    return [-1, -1, -1];
  }
}