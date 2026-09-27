// Run with TYPESAFE_API_KEY set to make one live Jev evaluation.
// HTTP attempts, including any configured retries, may be billed.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/drpaneas/jev"
)

type Team string

const (
	Billing   Team = "billing"
	Technical Team = "technical"
	Other     Team = "other"
)

// Declare and reuse questions; constructing them performs no network calls.
var department = jev.Choice("Which team should handle this ticket?",
	jev.Opt(Billing, "Payments, duplicate charges, invoices, subscriptions"),
	jev.Opt(Technical, "Bugs, outages and broken integrations"),
	jev.Opt(Other, "Anything not covered by the other teams"),
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	client, err := jev.New(os.Getenv("TYPESAFE_API_KEY"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var team jev.Decision[Team]
	var urgent jev.Probability
	var severity jev.Rating
	metadata, err := client.Evaluate(ctx,
		"I was charged twice and need someone to fix the invoice today.",
		jev.Into("department", department, &team),
		jev.Into("urgent", jev.Noul("Does the message express urgency?"), &urgent),
		jev.Into("severity", jev.Score("How disruptive is the reported problem?",
			"No disruption", "Disruption with a workaround", "Work is completely blocked"), &severity),
	)
	if err != nil {
		return err
	}

	// Illustrative thresholds: evaluate and tune against your own examples.
	if destination, ok := team.Resolve(0.90); ok {
		fmt.Println("Route to:", destination) // destination is Team, not string/any.
	} else {
		fmt.Println("Needs review; probabilities:", team.Probabilities())
	}
	if yes, ok := urgent.Resolve(0.90); ok {
		fmt.Println("Urgent:", yes) // false,true is a confident no.
	} else {
		fmt.Println("Urgency is uncertain")
	}
	score, _ := severity.Value() // Successful Evaluate guarantees availability.
	fmt.Printf("Severity: %.2f; confidence: %.2f\n", score, severity.Confidence())
	fmt.Printf("Model: %s; input tokens: %d\n", metadata.Model, metadata.Usage.InputTokens)
	return nil
}
