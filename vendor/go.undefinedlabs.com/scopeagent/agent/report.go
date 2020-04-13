package agent

import (
	"encoding/json"
	"fmt"
)

func (a *Agent) PrintReport() {
	a.printReportOnce.Do(func() {
		if a.recorder != nil && a.testingMode && a.recorder.stats.totalTestSpans > 0 {
			fmt.Printf("\n** Scope Test Report **\n")
			if a.recorder.stats.testSpansNotSent == 0 && a.recorder.stats.testSpansRejected == 0 {
				fmt.Println("Access the detailed test report for this build at:")
				fmt.Printf("   %s\n\n", a.getUrl(fmt.Sprintf("external/v1/results/%s", a.agentId)))
			} else {
				if !a.debugMode {
					a.logMetadata()
				}
				a.recorder.writeStats()
				fmt.Println("There was a problem sending data to Scope.")
				if a.recorder.stats.testSpansSent > 0 {
					fmt.Println("Partial results for this build are available at:")
					fmt.Printf("   %s\n\n", a.getUrl(fmt.Sprintf("external/v1/results/%s", a.agentId)))
				}
				fmt.Printf("Check the agent logs at %s for more information.\n\n", a.recorderFilename)
			}
		}
	})
}

func (a *Agent) logMetadata() {
	metaBytes, _ := json.Marshal(a.metadata)
	strMetadata := string(metaBytes)
	a.logger.Println("Agent Metadata:", strMetadata)
}
