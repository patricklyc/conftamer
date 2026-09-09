package main

type Config struct {
	/* Info about the module code */

	// Module as in go.mod
	Module_prefix string `yaml:"module_prefix"`
	// Location of the unmarshal function definition (optional)
	Unmarshal_fn string `yaml:"unmarshal_fn"`
	// Location of the unmarshal interface definition (optional)
	Unmarshal_iface string `yaml:"unmarshal_iface"`

	/* Customizing how to run the tool */

	// Directory for all results
	Output_path string `yaml:"output_path"`
	// Whether to find accessors too
	Find_accessors string `yaml:"find_accessors"`
	// Path to module source code
	Module_path string `yaml:"module_path"`
	// Path to the gopls code (should end in /gopls)
	Gopls_path string `yaml:"gopls_path"`
}
