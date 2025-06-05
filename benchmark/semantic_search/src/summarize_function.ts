
import { getConfig } from "./conf.js";

const AZURE_CONFIG = {
  apiKey: getConfig().azureApiKey,
  endpoint: getConfig().azureEndpoint,
  deploymentName: getConfig().azureDeploymentName
};

/**
 * Generates a concise summary for a function body
 * 
 * @param functionBody - The code of the function body to summarize
 * @returns A string containing a brief summary (1-sentence or few words)
 */
export async function summarizeFunctionBody(functionBody: string): Promise<string> {
  try {
    const prompt = createSummaryPrompt();
    const fullPrompt = `${prompt}\n\nFunction Body:\n\`\`\`\n${functionBody}\n\`\`\``;
    
    const response = await fetch(`${AZURE_CONFIG.endpoint}/openai/deployments/${AZURE_CONFIG.deploymentName}/chat/completions?api-version=2023-05-15`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'api-key': AZURE_CONFIG.apiKey
      },
      body: JSON.stringify({
        messages: [
          {
            role: 'system',
            content: 'You are an expert code analyzer that creates concise function summaries.'
          },
          {
            role: 'user',
            content: fullPrompt
          }
        ],
        temperature: 0.3,
        max_tokens: 100
      })
    });
    
    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`API request failed with status ${response.status}: ${errorText}`);
    }
    
    const result = await response.json();
    return result.choices[0].message.content.trim();
  } catch (error) {
    console.error("Error generating function summary:", error);
    return "";
  }
}

/**
 * Creates the prompt for GPT-4o to generate function summaries
 * 
 * @returns The prompt text for summarizing functions
 */
function createSummaryPrompt(): string {
  return `You are an expert code analyzer specialized in generating concise function summaries.

Task: Create a brief, informative summary for the provided function body.

Guidelines:
1. Keep your summary extremely concise - either:
   - A single short sentence (2-8 words)
   - A few descriptive keywords
   - A proposed function name that accurately describes what the function does

2. Focus exclusively on the function's purpose and behavior

3. Do not include:
   - Implementation details
   - Parameter names
   - Variable descriptions
   - How the function works internally

4. Your summary should help a developer quickly understand what the function accomplishes without having to read its code

5. Format your response as plain text with no additional explanations

Examples of good summaries:
- Calculates total price with tax
- User authentication validator
- Processes and filters image data
- fetchUserPreferences
- sanitize input, validate format

Analyze the following function body and provide your concise summary:`;
}

/**
 * Example function to test the summarization
 */
async function testSummarization() {
  const sampleFunction = `
  { const Extension* extension = registry()->GetExtensionById(extension_id_, ExtensionRegistry::ENABLED); const SettingsOverrides* settings = extension ? SettingsOverrides::Get(extension) : nullptr; if (!extension || !settings) { NOTREACHED(); } bool home_change = settings->homepage.has_value(); bool startup_change = !settings->startup_pages.empty(); bool search_change = settings->search_engine.has_value(); int first_line_id = 0; int second_line_id = 0; std::u16string body; switch (type_) { case BUBBLE_TYPE_HOME_PAGE: first_line_id = anchored_to_browser_action ? IDS_EXTENSIONS_SETTINGS_API_FIRST_LINE_HOME_PAGE_SPECIFIC : IDS_EXTENSIONS_SETTINGS_API_FIRST_LINE_HOME_PAGE; if (startup_change && search_change) { second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_START_AND_SEARCH; } else if (startup_change) { second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_START_PAGES; } else if (search_change) { second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_SEARCH_ENGINE; } break; case BUBBLE_TYPE_STARTUP_PAGES: first_line_id = anchored_to_browser_action ? IDS_EXTENSIONS_SETTINGS_API_FIRST_LINE_START_PAGES_SPECIFIC : IDS_EXTENSIONS_SETTINGS_API_FIRST_LINE_START_PAGES; if (home_change && search_change) { second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_HOME_AND_SEARCH; } else if (home_change) { second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_HOME_PAGE; } else if (search_change) { second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_SEARCH_ENGINE; } break; case BUBBLE_TYPE_SEARCH_ENGINE: first_line_id = anchored_to_browser_action ? IDS_EXTENSIONS_SETTINGS_API_FIRST_LINE_SEARCH_ENGINE_SPECIFIC : IDS_EXTENSIONS_SETTINGS_API_FIRST_LINE_SEARCH_ENGINE; if (startup_change && home_change) second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_START_AND_HOME; else if (startup_change) second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_START_PAGES; else if (home_change) second_line_id = IDS_EXTENSIONS_SETTINGS_API_SECOND_LINE_HOME_PAGE; break; } DCHECK_NE(0, first_line_id); body = anchored_to_browser_action ? l10n_util::GetStringUTF16(first_line_id) : l10n_util::GetStringFUTF16(first_line_id, base::UTF8ToUTF16(extension->name())); if (second_line_id) body += l10n_util::GetStringUTF16(second_line_id); body += l10n_util::GetStringUTF16( IDS_EXTENSIONS_SETTINGS_API_THIRD_LINE_CONFIRMATION); return body; }
  `;
  
  const summary = await summarizeFunctionBody(sampleFunction);
  console.log("Function Summary:", summary);
}

// testSummarization().catch(console.error);

