import Parser from 'tree-sitter';
import CPP from 'tree-sitter-cpp';

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
  
  // Collect all .cc files from the specified directories
  const allCcFiles: string[] = [];
  
  for (const dir of directories) {
    try {
      // Ensure the directory exists
      if (!fs.existsSync(dir) || !fs.statSync(dir).isDirectory()) {
        console.warn(`Skipping invalid directory: ${dir}`);
        continue;
      }
      
      // Get all files in the directory and its subdirectories
      const getFilesRecursively = (directory: string): string[] => {
        const files: string[] = [];
        const entries = fs.readdirSync(directory, { withFileTypes: true });
        
        for (const entry of entries) {
          const fullPath = path.join(directory, entry.name);
          
          if (entry.isDirectory()) {
            files.push(...getFilesRecursively(fullPath));          
          } else if (entry.isFile() &&  path.extname(entry.name) === '.cc' && !entry.name.toLowerCase().includes('test')) {
            files.push(fullPath);
          }
        }
        
        return files;
      };
      
      allCcFiles.push(...getFilesRecursively(dir));
    } catch (error) {
      console.error(`Error processing directory ${dir}:`, error);
    }
  }
  
  // Check if we found any .cc files
  if (allCcFiles.length === 0) {
    return [];
  }
  
  // If we don't have enough files, return all of them
  if (allCcFiles.length <= count) {
    return allCcFiles;
  }
  
  // Randomly select N files
  const selectedFiles: string[] = [];
  const availableFiles = [...allCcFiles];
  
  for (let i = 0; i < count && availableFiles.length > 0; i++) {
    const randomIndex = Math.floor(Math.random() * availableFiles.length);
    selectedFiles.push(availableFiles[randomIndex]);
    availableFiles.splice(randomIndex, 1); // Remove the selected file to avoid duplicates
  }
  
  return selectedFiles;
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

// main();

// Example usage:
// const directories = ['D:\\Edge\\src\\chrome\\browser\\ui\\tabs'];
// const randomFiles = getRandomCcFiles(directories, 100);
// console.log(randomFiles);