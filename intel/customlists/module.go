package customlists

import (
	"context"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/safing/portbase/api"
	"github.com/safing/portbase/modules"
)

var module *modules.Module

const (
	configModuleName  = "config"
	configChangeEvent = "config change"
)

// Helper variables for parsing the input file.
var (
	isCountryCode      = regexp.MustCompile("^[A-Z]{2}$").MatchString
	isAutonomousSystem = regexp.MustCompile(`^AS[0-9]+$`).MatchString
)

var (
	filterListFilePath         string
	filterListFileModifiedTime time.Time

	filterListLock sync.RWMutex
	parserTask     *modules.Task
)

func init() {
	module = modules.Register("customlists", prep, start, nil, "base")
}

func prep() error {
	initFilterLists()

	// Register the config in the ui.
	err := registerConfig()
	if err != nil {
		return err
	}

	return nil
}

func start() error {
	// Register to hook to update after config change.
	if err := module.RegisterEventHook(
		configModuleName,
		configChangeEvent,
		"update custom filter list",
		func(ctx context.Context, obj interface{}) error {
			checkAndUpdateFilterList()
			return nil
		},
	); err != nil {
		return err
	}

	// Create parser task and enqueue for execution. "checkAndUpdateFilterList" will schedule the next execution.
	parserTask = module.NewTask("intel/customlists:file-update-check", func(context.Context, *modules.Task) error {
		checkAndUpdateFilterList()
		return nil
	}).Schedule(time.Now().Add(20 * time.Second))

	// Register api endpoint for updating the filter list.
	if err := api.RegisterEndpoint(api.Endpoint{
		Path:      "customlists/update",
		Write:     api.PermitUser,
		BelongsTo: module,
		ActionFunc: func(ar *api.Request) (msg string, err error) {
			checkAndUpdateFilterList()
			return "Custom filter list loaded successfully.", nil
		},
		Name:        "Update custom filter list",
		Description: "Reload the filter list from the configured file.",
	}); err != nil {
		return err
	}

	return nil
}

func checkAndUpdateFilterList() {
	filterListLock.Lock()
	defer filterListLock.Unlock()

	// Get path and ignore if empty
	filePath := getFilePath()
	if filePath == "" {
		return
	}

	// Schedule next update check
	parserTask.Schedule(time.Now().Add(1 * time.Minute))

	// Try to get file info
	modifiedTime := time.Now()
	if fileInfo, err := os.Stat(filePath); err == nil {
		modifiedTime = fileInfo.ModTime()
	}

	// Check if file path has changed or if modified time has changed
	if filterListFilePath != filePath || !filterListFileModifiedTime.Equal(modifiedTime) {
		err := parseFile(filePath)
		if err != nil {
			return
		}
		filterListFileModifiedTime = modifiedTime
		filterListFilePath = filePath
	}
}

// LookupIP checks if the IP address is in a custom filter list.
func LookupIP(ip net.IP) bool {
	filterListLock.RLock()
	defer filterListLock.RUnlock()

	_, ok := ipAddressesFilterList[ip.String()]
	return ok
}

// LookupDomain checks if the Domain is in a custom filter list.
func LookupDomain(fullDomain string, filterSubdomains bool) (bool, string) {
	filterListLock.RLock()
	defer filterListLock.RUnlock()

	if filterSubdomains {
		// Check if domain is in the list and all its subdomains.
		listOfDomains := splitDomain(fullDomain)
		for _, domain := range listOfDomains {
			_, ok := domainsFilterList[domain]
			if ok {
				return true, domain
			}
		}
	} else {
		// Check only if the domain is in the list
		_, ok := domainsFilterList[fullDomain]
		return ok, fullDomain
	}
	return false, ""
}

// LookupASN checks if the Autonomous system number is in a custom filter list.
func LookupASN(number uint) bool {
	filterListLock.RLock()
	defer filterListLock.RUnlock()

	_, ok := autonomousSystemsFilterList[number]
	return ok
}

// LookupCountry checks if the country code is in a custom filter list.
func LookupCountry(countryCode string) bool {
	filterListLock.RLock()
	defer filterListLock.RUnlock()

	_, ok := countryCodesFilterList[countryCode]
	return ok
}

func splitDomain(domain string) []string {
	domain = strings.Trim(domain, ".")
	suffix, _ := publicsuffix.PublicSuffix(domain)
	if suffix == domain {
		return []string{domain}
	}

	domainWithoutSuffix := domain[:len(domain)-len(suffix)]
	domainWithoutSuffix = strings.Trim(domainWithoutSuffix, ".")

	splitted := strings.FieldsFunc(domainWithoutSuffix, func(r rune) bool {
		return r == '.'
	})

	domains := make([]string, 0, len(splitted))
	for idx := range splitted {

		d := strings.Join(splitted[idx:], ".") + "." + suffix
		if d[len(d)-1] != '.' {
			d += "."
		}
		domains = append(domains, d)
	}
	return domains
}
