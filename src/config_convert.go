package main

import (
	"fmt"
	"io/ioutil"
	"regexp"
	"strings"

	"github.com/newrelic/infra-integrations-sdk/log"
	yaml "gopkg.in/yaml.v2"
)

var defaultEventType = "JMXSample"
var sep = "_"

// 'Parser' structs for parsing Java Agent YAML format for JMX counters

type mainDefinitionParser struct {
	Name    string                `yaml:"name"`
	Version float32               `yaml:"version"`
	Enabled bool                  `yaml:enabled`
	JMX     []jmxDefinitionParser `yaml:"jmx"`
}

type jmxDefinitionParser struct {
	ObjectName     string                    `yaml:"object_name"`
	RootMetricName string                    `yaml:"root_metric_name"`
	Metrics        []metricsDefinitionParser `yaml:"metrics"`
}

type metricsDefinitionParser struct {
	Attributes string `yaml:"attributes"`
	Type       string `yaml:"type"`
}

// 'Reducer' structs for reducing down from old format into minimized version of new format

type domainReducer struct {
	EventType string
	BeansMap  map[string]*beanReducer // String is 'query'
}

type beanReducer struct {
	AttributesMap map[string]*attributeReducer // String is 'attr'
}

type attributeReducer struct {
	MetricType string
	MetricName string // In case we want to have a different metric name than 'attr'
}

// 'Output' structs for marshaling into yaml output format of nri-jmx

type collectOutput struct {
	Collect []*domainOutput `yaml:"collect"`
}

type domainOutput struct {
	Domain    string        `yaml:"domain"`
	EventType string        `yaml:"event_type"`
	Beans     []*beanOutput `yaml:"beans"`
}

type beanOutput struct {
	Query      string             `yaml:"query"`
	Attributes []*attributeOutput `yaml:"attributes"`
}

type attributeOutput struct {
	Attr       string `yaml:"attr"`
	MetricType string `yaml:"metric_type"`
	MetricName string `yaml:"metric_name"`
}

func parseJavaAgentYaml(filename string) (*mainDefinitionParser, error) {
	yamlFile, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Error("failed to open %s: %s", filename, err)
		return nil, err
	}
	var m mainDefinitionParser
	if err := yaml.Unmarshal(yamlFile, &m); err != nil {
		log.Error("failed to parse collection: %s", err)
		return nil, err
	}
	return &m, nil
}

// Simple Parse: Does not "map/reduce" domains and queries
func parseJavaAgentJmxDefinition(m *mainDefinitionParser) ([]*domainOutput, error) {
	var domains []*domainOutput

	for _, jmxObject := range m.JMX {
		var domainAndQuery = strings.Split(jmxObject.ObjectName, ":")
		var outbeans []*beanOutput
		for _, thisMetric := range jmxObject.Metrics {
			var inAttrs = strings.Split(thisMetric.Attributes, ",")
			var outAttrs []*attributeOutput
			for _, thisAttr := range inAttrs {
				thisAttr = strings.TrimSpace(thisAttr)
				outAttrs = append(outAttrs, &attributeOutput{Attr: thisAttr, MetricType: convertMetricType(thisMetric.Type), MetricName: thisAttr})
			}
			outbeans = append(outbeans, &beanOutput{Query: domainAndQuery[1], Attributes: outAttrs})
		}
		var outEventType = getEventName(m.Name, jmxObject.RootMetricName, domainAndQuery[1])
		domains = append(domains, &domainOutput{Domain: domainAndQuery[0], EventType: outEventType, Beans: outbeans})
	}
	return domains, nil
}

// Reducing Parse: maps, reduces and organizes domains, queries and attributes
func reduceJavaAgentYaml(m *mainDefinitionParser) (map[string]*domainReducer, error) {
	thisDomainMap := make(map[string]*domainReducer)
	for _, jmxObject := range m.JMX {
		var thisDomain *domainReducer
		var thisBean *beanReducer
		var domainAndQuery = strings.Split(jmxObject.ObjectName, ":")
		if _, ok := thisDomainMap[domainAndQuery[0]]; ok {
			thisDomain = thisDomainMap[domainAndQuery[0]]
			if _, ok := thisDomain.BeansMap[domainAndQuery[1]]; ok {
				thisBean = thisDomain.BeansMap[domainAndQuery[1]]
			}
		}
		for _, thisMetric := range jmxObject.Metrics {
			var inAttrs = strings.Split(thisMetric.Attributes, ",")
			for _, thisAttr := range inAttrs {
				thisAttr = strings.TrimSpace(thisAttr)
				if thisBean != nil {
					if _, ok := thisBean.AttributesMap[thisAttr]; !ok {
						thisBean.AttributesMap[thisAttr] = &attributeReducer{MetricType: convertMetricType(thisMetric.Type), MetricName: thisAttr}
					}
				} else {
					thisAttrMap := make(map[string]*attributeReducer)
					thisAttrMap[thisAttr] = &attributeReducer{MetricType: convertMetricType(thisMetric.Type), MetricName: thisAttr}
					thisBean = &beanReducer{AttributesMap: thisAttrMap}
					if thisDomain == nil {
						var outEventType = getEventName(m.Name, jmxObject.RootMetricName, domainAndQuery[1])
						thisBeanMap := make(map[string]*beanReducer)
						thisBeanMap[domainAndQuery[1]] = thisBean
						thisDomainMap[domainAndQuery[0]] = &domainReducer{EventType: outEventType, BeansMap: thisBeanMap}
					} else {
						thisDomain.BeansMap[domainAndQuery[1]] = thisBean
					}
				}
			}
		}
	}
	return thisDomainMap, nil
}

// Builds nri-jmx-compatible yaml from mapped/reduced parse of Java Agent yaml
func buildCollectionDefinition(dr map[string]*domainReducer) ([]*domainOutput, error) {
	var domains []*domainOutput
	for domain, domainContents := range dr {
		var beans []*beanOutput
		for bean, beanContents := range domainContents.BeansMap {
			var attributes []*attributeOutput
			for attribute, attributeContents := range beanContents.AttributesMap {
				attributes = append(attributes, &attributeOutput{Attr: attribute, MetricType: attributeContents.MetricType, MetricName: attributeContents.MetricName})
			}
			beans = append(beans, &beanOutput{Query: bean, Attributes: attributes})
		}
		domains = append(domains, &domainOutput{Domain: domain, EventType: domainContents.EventType, Beans: beans})
	}
	return domains, nil
}

// Spits out the nri-jmx-compatible yaml file
func outputOHIJmxFile(filename string, d []*domainOutput) {
	log.Info("New File: " + filename + ".new\n")
	m, err := yaml.Marshal(&collectOutput{Collect: d})
	if err != nil {
		fmt.Printf("error: %v", err)
	}
	fmt.Printf("%s", string(m))
}

func convertMetricType(metrictype string) string {
	switch strings.TrimSpace(metrictype) {
	case "simple":
		return "gauge"
	case "monotonically_increasing":
		return "delta"
	default:
		return "gauge"
	}
}

func getEventName(oldName string, rootMetricName string, query string) string {
	if oldName == "" {
		oldName = defaultEventType
	}

	if rootMetricName == "" {
		return makeInsightsCompliantEventType(oldName)
	}

	objNameRegex, _ := regexp.Compile("{\\w+}")
	if objNameRegex.MatchString(rootMetricName) {
		var queryStrings = strings.Split(query, ",")
		queryMap := make(map[string]string)
		for _, thisQuery := range queryStrings {
			var querySplit = strings.Split(thisQuery, "=")
			queryMap[querySplit[0]] = querySplit[1]
		}
		var matchedObjs = objNameRegex.FindAllString(rootMetricName, -1)
		for _, thisObj := range matchedObjs {
			var testObj = thisObj[1 : len(thisObj)-1]
			if objVal, ok := queryMap[testObj]; ok {
				rootMetricName = strings.Replace(rootMetricName, thisObj, objVal, -1)
			}
		}
	}
	return makeInsightsCompliantEventType(oldName + sep + rootMetricName)
}

func makeInsightsCompliantEventType(inString string) string {
	inString = strings.Replace(inString, " ", "_", -1)
	inString = strings.Replace(inString, "/", ":", -1)
	return inString
}
