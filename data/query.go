package data

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// timeToMinutes converts a time string like "-14d" to minutes
func timeToMinutes(time string) (float64, error) {
	timeUnits := map[string]float64{
		"s": 1.0 / 60.0,
		"m": 1.0,
		"h": 60.0,
		"d": 1440.0,
		"w": 10080.0,
		"M": 43200.0,
		"y": 525600.0,
	}

	// Remove the "-" sign if present
	time = strings.TrimPrefix(time, "-")

	if len(time) < 2 {
		return 0, fmt.Errorf("invalid time format:  %s", time)
	}

	unit := string(time[len(time)-1])
	valueStr := time[:len(time)-1]

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid time value: %s", valueStr)
	}

	multiplier, ok := timeUnits[unit]
	if !ok {
		return 0, fmt.Errorf("invalid time unit: %s", unit)
	}

	return value * multiplier, nil
}

// minutesToTime converts minutes back to a readable time unit
func minutesToTime(minutes float64) string {
	type unit struct {
		symbol  string
		minutes float64
	}

	// Units in descending order
	units := []unit{
		{"y", 525600.0},
		{"M", 43200.0},
		{"w", 10080.0},
		{"d", 1440.0},
		{"h", 60.0},
		{"m", 1.0},
		{"s", 1.0 / 60.0},
	}

	// Find the best matching unit
	for _, u := range units {
		if minutes >= u.minutes {
			value := math.Ceil(minutes / u.minutes)
			return fmt.Sprintf("%.0f%s", value, u.symbol)
		}
	}

	// Fallback to seconds
	seconds := math.Ceil(minutes * 60)
	return fmt.Sprintf("%.0f%s", seconds, "s")
}

// convertToInfluxDuration converts time format like "4m", "1h", "1d" to InfluxDB duration format
func convertToInfluxDuration(step string) (string, error) {
	if len(step) < 2 {
		return "", fmt.Errorf("invalid step format: %s", step)
	}

	unit := string(step[len(step)-1])
	valueStr := step[:len(step)-1]

	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return "", fmt.Errorf("invalid step value: %s", valueStr)
	}

	// Convert to InfluxDB duration format
	switch unit {
	case "s":
		return fmt.Sprintf("%.0fs", value), nil
	case "m":
		return fmt.Sprintf("%.0fm", value), nil
	case "h":
		return fmt.Sprintf("%.0fh", value), nil
	case "d":
		return fmt.Sprintf("%.0fd", value), nil
	case "w":
		return fmt.Sprintf("%.0fw", value), nil
	case "M":
		return fmt.Sprintf("%.0fmo", value), nil
	case "y":
		return fmt.Sprintf("%.0fy", value), nil
	default:
		return "", fmt.Errorf("invalid time unit: %s", unit)
	}
}

// CalculateOptimalStep calculates the optimal step for a given time range
// Uses adaptive strategy to handle cases where actual data might be less than requested range
func CalculateOptimalStep(start string, maxDataPoints int) (string, error) {
	return CalculateOptimalStepWithMin(start, maxDataPoints, 10)
}

// CalculateOptimalStepWithMin calculates the optimal step for a given time range
// Uses adaptive strategy to ensure meaningful data points even with sparse data
func CalculateOptimalStepWithMin(start string, maxDataPoints, minDataPoints int) (string, error) {
	if maxDataPoints <= 0 {
		maxDataPoints = 360
	}
	if minDataPoints <= 0 {
		minDataPoints = 10
	}

	rangeInMinutes, err := timeToMinutes(start)
	if err != nil {
		return "", err
	}

	rangeInMinutes = math.Abs(rangeInMinutes)

	// Calculate step based on maxDataPoints
	stepForMaxPoints := rangeInMinutes / float64(maxDataPoints)

	// Calculate step based on minDataPoints
	stepForMinPoints := rangeInMinutes / float64(minDataPoints)

	// Define reasonable step sizes based on query range
	// This ensures we get granular enough data even when querying large ranges with sparse data
	var reasonableStep float64

	switch {
	case rangeInMinutes <= 60: // <= 1 hour
		reasonableStep = 10.0 / 60.0 // 10 seconds
	case rangeInMinutes <= 360: // <= 6 hours
		reasonableStep = 1.0 // 1 minute
	case rangeInMinutes <= 1440: // <= 1 day
		reasonableStep = 4.0 // 4 minutes
	case rangeInMinutes <= 10080: // <= 1 week
		reasonableStep = 30.0 // 30 minutes
	case rangeInMinutes <= 43200: // <= 1 month
		reasonableStep = 360.0 // 6 hours
	default: // > 1 month
		reasonableStep = 1440.0 // 1 day
	}

	// Choose the minimum of these three strategies:
	// 1. Step for max data points (don't exceed limit)
	// 2. Step for min data points (ensure minimum)
	// 3. Reasonable step for this time range (handle sparse data)
	stepInMinutes := math.Min(stepForMaxPoints, math.Min(stepForMinPoints, reasonableStep))

	// Ensure step is at least 1 second
	minStepMinutes := 1.0 / 60.0
	if stepInMinutes < minStepMinutes {
		stepInMinutes = minStepMinutes
	}

	return minutesToTime(stepInMinutes), nil
}

// CalculateDataPoints calculates the number of data points
func CalculateDataPoints(start, step string) (int, error) {
	rangeInMinutes, err := timeToMinutes(start)
	if err != nil {
		return 0, err
	}

	stepInMinutes, err := timeToMinutes(step)
	if err != nil {
		return 0, err
	}

	rangeInMinutes = math.Abs(rangeInMinutes)
	dataPoints := rangeInMinutes / stepInMinutes

	return int(math.Ceil(dataPoints)), nil
}

// RoundToNiceStep rounds the step to "nice" values
func RoundToNiceStep(step string) (string, error) {
	minutes, err := timeToMinutes(step)
	if err != nil {
		return "", err
	}

	// Define "nice" intervals in minutes
	niceIntervals := []float64{
		1.0 / 60.0,  // 1s
		5.0 / 60.0,  // 5s
		10.0 / 60.0, // 10s
		15.0 / 60.0, // 15s
		30.0 / 60.0, // 30s
		1.0,         // 1m
		2.0,         // 2m
		5.0,         // 5m
		10.0,        // 10m
		15.0,        // 15m
		30.0,        // 30m
		60.0,        // 1h
		120.0,       // 2h
		180.0,       // 3h
		360.0,       // 6h
		720.0,       // 12h
		1440.0,      // 1d
		2880.0,      // 2d
		4320.0,      // 3d
		10080.0,     // 1w
		20160.0,     // 2w
		43200.0,     // 1M
	}

	// Find the next "nice" interval
	for _, interval := range niceIntervals {
		if minutes <= interval {
			return minutesToTime(interval), nil
		}
	}

	return minutesToTime(minutes), nil
}

// getAdaptiveStep returns an appropriate step size that adapts to potential sparse data
func getAdaptiveStep(start string, maxDataPoints int) (string, error) {
	rangeInMinutes, err := timeToMinutes(start)
	if err != nil {
		return "", err
	}
	rangeInMinutes = math.Abs(rangeInMinutes)

	// For short ranges, use fine-grained steps
	if rangeInMinutes <= 1440 { // <= 1 day
		switch {
		case rangeInMinutes <= 60: // <= 1 hour
			return "10s", nil
		case rangeInMinutes <= 360: // <= 6 hours
			return "1m", nil
		default: // <= 1 day
			return "4m", nil
		}
	}

	// For longer ranges, calculate a step that would give us maxDataPoints
	// if we only had 1 day of actual data
	// This ensures we get granular data even with sparse datasets
	assumedDataMinutes := math.Min(rangeInMinutes, 1440.0) // Assume at most 1 day of data
	stepInMinutes := assumedDataMinutes / float64(maxDataPoints)

	// Round up to nice intervals
	switch {
	case stepInMinutes <= 1:
		return "1m", nil
	case stepInMinutes <= 5:
		return "5m", nil
	case stepInMinutes <= 15:
		return "15m", nil
	case stepInMinutes <= 30:
		return "30m", nil
	case stepInMinutes <= 60:
		return "1h", nil
	case stepInMinutes <= 120:
		return "2h", nil
	case stepInMinutes <= 360:
		return "4h", nil
	case stepInMinutes <= 720:
		return "6h", nil
	default:
		return "12h", nil
	}
}

// BuildInfluxQuery builds an InfluxDB Flux query for player count data
// Parameters:
//   - start: time range like "-1d", "-7d", etc.
//   - step: aggregation window like "4m", "1h", etc.
//   - serverFilter: optional server name filter (empty string for all servers)
func BuildInfluxQuery(start, step, serverFilter string) (string, error) {
	// Convert step to InfluxDB duration format
	windowDuration, err := convertToInfluxDuration(step)
	if err != nil {
		return "", err
	}

	// Build the base query
	query := fmt.Sprintf(`from(bucket: "minetracker_data")
  |> range(start:  %s)
  |> filter(fn: (r) => r["_measurement"] == "server_data")
  |> filter(fn: (r) => r["_field"] == "player_count")`, start)

	// Add server filter if specified
	if serverFilter != "" {
		query += fmt.Sprintf(`
  |> filter(fn:  (r) => r["ip"] == "%s")`, serverFilter)
	}

	// Add aggregation window
	// createEmpty: false ensures only windows with actual data are returned
	// This is crucial for handling sparse data scenarios
	query += fmt.Sprintf(`
  |> aggregateWindow(every: %s, fn:  mean, createEmpty: false)
  |> yield(name: "mean")`, windowDuration)

	return query, nil
}

// BuildInfluxQueryWithOptimalStep builds an InfluxDB Flux query with automatically calculated optimal step
// to ensure the result stays under maxDataPoints but returns at least minDataPoints
func BuildInfluxQueryWithOptimalStep(start, serverFilter string, maxDataPoints int) (string, error) {
	return BuildInfluxQueryWithOptimalStepAndMin(start, serverFilter, maxDataPoints, 10)
}

// BuildInfluxQueryWithOptimalStepAndMin builds an InfluxDB Flux query with automatically calculated optimal step
// ensuring at least minDataPoints are returned
func BuildInfluxQueryWithOptimalStepAndMin(start, serverFilter string, maxDataPoints, minDataPoints int) (string, error) {
	// Calculate optimal step with minimum data points
	step, err := CalculateOptimalStepWithMin(start, maxDataPoints, minDataPoints)
	if err != nil {
		return "", err
	}

	// Optionally round to nice step
	niceStep, err := RoundToNiceStep(step)
	if err != nil {
		return "", err
	}

	// Build the query with the calculated step
	return BuildInfluxQuery(start, niceStep, serverFilter)
}

// QueryParams holds the parameters for building an InfluxDB query
type QueryParams struct {
	Start         string // Time range like "-1d", "-7d"
	Step          string // Aggregation window like "4m", "1h" (optional, will be calculated if empty)
	ServerFilter  string // Server name filter (optional)
	MaxDataPoints int    // Maximum number of data points (default: 360)
	MinDataPoints int    // Minimum number of data points (default: 10)
	UseAdaptive   bool   // Use adaptive step calculation (recommended for sparse data)
}

// BuildInfluxQueryFromParams builds an InfluxDB Flux query from QueryParams
func BuildInfluxQueryFromParams(params QueryParams) (string, int, string, error) {
	// Set default max data points if not specified
	if params.MaxDataPoints <= 0 {
		params.MaxDataPoints = 360
	}

	// Set default min data points if not specified
	if params.MinDataPoints <= 0 {
		params.MinDataPoints = 10
	}

	var step string
	var err error

	// If step is not provided, calculate optimal step
	if params.Step == "" {
		if params.UseAdaptive {
			// Use adaptive mode:  choose step based on time range
			// This is better for handling sparse data scenarios
			step, err = getAdaptiveStep(params.Start, params.MaxDataPoints)
			if err != nil {
				return "", 0, step, err
			}
		} else {
			// Use calculated mode: balance between min and max data points
			step, err = CalculateOptimalStepWithMin(params.Start, params.MaxDataPoints, params.MinDataPoints)
			if err != nil {
				return "", 0, step, err
			}

			// Round to nice step
			step, err = RoundToNiceStep(step)
			if err != nil {
				return "", 0, step, err
			}
		}
	} else {
		step = params.Step
	}

	// Calculate actual data points (estimated based on requested range)
	dataPoints, err := CalculateDataPoints(params.Start, step)
	if err != nil {
		return "", 0, step, err
	}

	// Build the query
	query, err := BuildInfluxQuery(params.Start, step, params.ServerFilter)
	if err != nil {
		return "", 0, step, err
	}

	return query, dataPoints, step, nil
}
