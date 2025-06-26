import Parser from 'tree-sitter';
import CPP from 'tree-sitter-cpp';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);

interface FunctionInfo {
  name: string;
  body: string;
  startLine: number;
  endLine: number;
  file: string;
}

export function parseFileRange(input: string) {
  const match = input.match(/^(.*):(\d+)-(\d+)$/);
  if (!match) {
    throw new Error("Invalid input format: expected 'path:line1-line2'");
  }

  return {
    path: match[1],
    startLine: parseInt(match[2], 10),
    endLine: parseInt(match[3], 10),
  };
}

import * as fs from 'fs';
import * as path from 'path';

/**
 * Randomly selects N files with .cc extension from the provided directories
 * using reservoir sampling with balanced selection from all directories
 * @param directories Array of directory paths to search in
 * @param count Number of files to select
 * @returns Array of selected file paths
 */
export function getRandomCcFiles(directories: string[], count: number): string[] {
  // Validate inputs
  if (!Array.isArray(directories) || directories.length === 0) {
    throw new Error('Directories must be a non-empty array');
  }
  
  if (count <= 0) {
    throw new Error('Count must be a positive number');
  }
  
  // Initialize reservoir for sampling
  const reservoir: string[] = [];
  let filesProcessed = 0;
  
  // Process directories in random order to ensure fair sampling
  const shuffledDirs = [...directories].sort(() => Math.random() - 0.5);
  
  // Set a per-directory limit on files to check to ensure we don't spend too much time in any one directory
  const MAX_FILES_PER_DIR = Math.max(50, Math.ceil(count * 50 / directories.length));
  // Total file limit as a safety measure
  const TOTAL_MAX_FILES = count * 500;
  
  // First pass: collect a sample from each directory to ensure representation
  for (const dir of shuffledDirs) {
    if (filesProcessed >= TOTAL_MAX_FILES) break;
    
    try {
      // Ensure the directory exists
      if (!fs.existsSync(dir) || !fs.statSync(dir).isDirectory()) {
        console.warn(`Skipping invalid directory: ${dir}`);
        continue;
      }
      
      let filesInDir = 0;
      // Queue for directory traversal
      const dirQueue: string[] = [dir];
      const ccFilesInDir: string[] = [];
      
      // First collect a batch of files from this directory (up to our per-directory limit)
      while (dirQueue.length > 0 && filesInDir < MAX_FILES_PER_DIR) {
        const currentDir = dirQueue.shift()!;
        
        try {
          const entries = fs.readdirSync(currentDir, { withFileTypes: true });
          // Randomize entries to ensure variety in sampling
          entries.sort(() => Math.random() - 0.5);
          
          for (const entry of entries) {
            if (filesInDir >= MAX_FILES_PER_DIR) break;
            
            const fullPath = path.join(currentDir, entry.name);
            
            if (entry.isDirectory()) {
              // Add directories to the queue, but with lower priority (push to end)
              dirQueue.push(fullPath);
            } else if (
              entry.isFile() && 
              path.extname(entry.name) === '.cc' && 
              !entry.name.toLowerCase().includes('test')
            ) {
              // Collect CC files from this directory
              ccFilesInDir.push(fullPath);
              filesInDir++;
            }
          }
        } catch (error) {
          console.error(`Error reading directory ${currentDir}:`, error);
        }
      }
      
      // Randomly select some files from this directory and add to the reservoir
      // The number to select is proportional to the directory size relative to others
      if (ccFilesInDir.length > 0) {
        // Shuffle the files found in this directory
        ccFilesInDir.sort(() => Math.random() - 0.5);
        
        // Add sampled files to the reservoir using reservoir sampling
        for (const file of ccFilesInDir) {
          filesProcessed++;
          
          if (reservoir.length < count) {
            reservoir.push(file);
          } else {
            // Reservoir sampling algorithm
            const j = Math.floor(Math.random() * filesProcessed);
            if (j < count) {
              reservoir[j] = file;
            }
          }
          
          // Safety check for total files processed
          if (filesProcessed >= TOTAL_MAX_FILES) break;
        }
      }
    } catch (error) {
      console.error(`Error processing directory ${dir}:`, error);
    }
  }
  
  // If we couldn't find enough files, return what we have
  return reservoir;
}

/**
 * Parse a C++ file and extract all function declarations and definitions
 * @param filePath Path to the C++ file to parse
 * @returns An array of function information objects
 */
export function parseCppFunctions(filePath: string): FunctionInfo[] {
  // Read the file content
  const fileContent = fs.readFileSync(filePath, 'utf8');
  
  // Initialize the parser with the C++ grammar
  const parser = new Parser();
  parser.setLanguage(CPP);
  
  // Parse the source code
  let tree;
  try {
    tree = parser.parse(fileContent);
  } catch (error: any) {
    console.error(`Error parsing ${filePath}:`, error.message || String(error));
    return [];
  }
  
  // Array to store function information
  const functions: FunctionInfo[] = [];
  
  // Helper function to recursively traverse the AST
  function traverseTree(node: any, source: string) {
    // Check if the node is a function definition
    if (node.type === 'function_definition') {
      // Find the function declarator (contains the name)
      let functionName = '';
      let declaratorNode = node.children.find((child: any) => 
        child.type === 'function_declarator' || 
        child.type === 'template_function' || 
        child.type === 'declaration'
      );
      
      // If we found a declarator, try to extract the function name
      if (declaratorNode) {
        // For function_declarator, the name is typically in the first child
        const nameNode = declaratorNode.children.find((child: any) => 
          child.type === 'identifier' || 
          child.type === 'qualified_identifier'
        );
        
        if (nameNode) {
          functionName = source.substring(nameNode.startIndex, nameNode.endIndex);
        }
      }
      
      // Find the function body
      const bodyNode = node.children.find((child: any) => child.type === 'compound_statement');
      
      if (bodyNode && functionName !== '') {
        // Extract the function body text
        const bodyText = source.substring(bodyNode.startIndex, bodyNode.endIndex);
        
        // Calculate the line numbers
        const startLine = countLines(source.substring(0, node.startIndex)) + 1;
        const endLine = countLines(source.substring(0, node.endIndex));
          // Add the function information to our results
        functions.push({
          name: functionName,
          body: bodyText,
          startLine,
          endLine,
          file: filePath  // Store absolute file path instead of just basename
        });
      }
    }
    
    // Recursively process all child nodes
    for (const child of node.children) {
      traverseTree(child, source);
    }
  }
  
  // Start traversing from the root node
  traverseTree(tree.rootNode, fileContent);
  
  return functions;
}

/**
 * Count the number of lines in a string
 * @param text The text to count lines in
 * @returns The number of lines
 */
function countLines(text: string): number {
  return (text.match(/\n/g) || []).length;
}

/**
 * Main function that processes a file or directory
 * @param targetPath Path to a file or directory to process
 */
function main() {
  // Get the target path from command line arguments
  const targetPath = process.argv[2];
  
  if (!targetPath) {
    console.error('Please provide a path to a C++ file or directory');
    process.exit(1);
  }
  
  // Check if the path exists
  if (!fs.existsSync(targetPath)) {
    console.error(`Path does not exist: ${targetPath}`);
    process.exit(1);
  }
  
  const functions = parseCppFunctions(targetPath);
    
  console.log(`Found ${functions.length} functions in ${targetPath}:`);
  functions.forEach(func => {
    console.log(`\nFunction: ${func.name}`);
    console.log(`Lines: ${func.startLine}-${func.endLine}`);
    console.log('Body:');
    console.log(func.body);
    console.log('-'.repeat(50));
  });
}

if (path.resolve(__filename) === path.resolve(process.argv[1])) {
  const directories = ['D:\\project\\chromium-src-snapshot\\components', 'D:\\project\\chromium-src-snapshot\\chrome'];
  const randomFiles = getRandomCcFiles(directories, 20000);
  for (const file of randomFiles) {
    console.log(`${file}`);
  }
}