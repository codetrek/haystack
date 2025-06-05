import * as fs from 'fs';
import * as path from 'path';

/**
 * Configuration interface that defines all available configuration options
 */
export interface Config {
  azureApiKey: string;
  azureEndpoint: string;
  azureDeploymentName: string;

  workspace: string;
  haystackSymbolUrl: string;
}

/**
 * Default configuration values
 */
const defaultConfig: Config = {
  azureApiKey: '',
  azureEndpoint: '',
  azureDeploymentName: '',

  workspace: '',
  haystackSymbolUrl: '',
};

/**
 * Global configuration
 */
class Configuration {
  private config: Config;

  constructor() {
    this.config = { ...defaultConfig };
  }
  
  /**
   * Get the current configuration
   */
  public getConfig(): Readonly<Config> {
    return { ...this.config };
  }

  /**
   * Update the configuration with partial config
   * @param partialConfig Partial configuration to merge
   */
  public updateConfig(partialConfig: Partial<Config>): void {
    this.config = {
      ...this.config,
      ...partialConfig,
    };
  }

  /**
   * Load configuration from a JSON file
   * @param configPath Path to the JSON configuration file
   * @returns True if the file was loaded successfully, false otherwise
   */
  public loadFromFile(configPath: string): boolean {
    try {
      const resolvedPath = path.resolve(configPath);
      
      if (!fs.existsSync(resolvedPath)) {
        console.error(`Configuration file not found: ${resolvedPath}`);
        return false;
      }
      
      const fileContent = fs.readFileSync(resolvedPath, 'utf-8');
      const loadedConfig = JSON.parse(fileContent);
      
      this.updateConfig(loadedConfig);
      console.log(`Configuration loaded from: ${resolvedPath}`);
      return true;
    } catch (error) {
      console.error(`Failed to load configuration: ${(error as Error).message}`);
      return false;
    }
  }

  /**
   * Save current configuration to a JSON file
   * @param configPath Path to save the configuration file
   * @returns True if the file was saved successfully, false otherwise
   */
  public saveToFile(configPath: string): boolean {
    try {
      const resolvedPath = path.resolve(configPath);
      const dirPath = path.dirname(resolvedPath);
      
      if (!fs.existsSync(dirPath)) {
        fs.mkdirSync(dirPath, { recursive: true });
      }
      
      fs.writeFileSync(
        resolvedPath,
        JSON.stringify(this.config, null, 2),
        'utf-8'
      );
      
      console.log(`Configuration saved to: ${resolvedPath}`);
      return true;
    } catch (error) {
      console.error(`Failed to save configuration: ${(error as Error).message}`);
      return false;
    }
  }
}

// Export a singleton instance of the Configuration
export const conf = new Configuration();

// Export convenience methods
export const getConfig = () => conf.getConfig();
export const updateConfig = (partialConfig: Partial<Config>) => conf.updateConfig(partialConfig);
export const loadConfigFromFile = (configPath: string) => conf.loadFromFile(configPath);
export const saveConfigToFile = (configPath: string) => conf.saveToFile(configPath);