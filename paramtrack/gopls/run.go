package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/emilykmarx/conftamer/paramtrack/util"
)

// Run gopls to get CTypes
func main() {
	var config_file string
	flag.StringVar(&config_file, "config", "", "Path to config file")
	flag.Parse()

	config := Config{}
	err := util.LoadConfig(config_file, &config)
	util.CheckCmd(nil, err)

	// Setup
	out, err := exec.Command("mkdir", "-p", config.Output_path).CombinedOutput()
	util.CheckCmd(out, err)

	err = os.Chdir(config.Gopls_path)
	util.CheckCmd(nil, err)
	out, err = exec.Command("go", "build", ".").CombinedOutput()
	util.CheckCmd(out, err)

	// Find Unmarshaler Subgraph, and optionally Accessors
	err = os.Chdir(config.Module_path)
	util.CheckCmd(nil, err)
	gopls_cmd := []string{"conftamer",
		"-module_prefix=" + config.Module_prefix,
		"-unmarshal_fn=" + config.Unmarshal_fn,
		"-unmarshal_iface=" + config.Unmarshal_iface,
		"-find_accessors=" + config.Find_accessors,
		"-output_path=" + config.Output_path,
	}

	fmt.Println(gopls_cmd)

	gopls := exec.Command(config.Gopls_path+"/gopls", gopls_cmd...)
	// get live results
	gopls.Stdout = os.Stdout
	gopls.Stderr = os.Stderr
	err = gopls.Run()
	util.CheckCmd(nil, err)
}
