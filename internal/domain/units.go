package domain

import "strings"

// NormalizeMeasurement 将外部试验单位转换为规则引擎使用的标准单位。
func NormalizeMeasurement(testType string, value Fixed, unit string) (Fixed, string, error) {
	testType = strings.ToUpper(strings.TrimSpace(testType))
	unit = strings.TrimSpace(unit)
	switch testType {
	case "TENSILE", "YIELD":
		switch strings.ToLower(unit) {
		case "mpa":
			return value, "MPa", nil
		case "kpa":
			return divideExact(value, 1000, "kPa 到 MPa 的换算结果超过三位小数")
		case "pa":
			return divideExact(value, 1000000, "Pa 到 MPa 的换算结果超过三位小数")
		default:
			return 0, "", Invalid("强度试验单位必须是 MPa、kPa 或 Pa", unit)
		}
	case "IMPACT":
		switch strings.ToLower(unit) {
		case "j":
			return value, "J", nil
		case "kj":
			return value * 1000, "J", nil
		default:
			return 0, "", Invalid("冲击试验单位必须是 J 或 kJ", unit)
		}
	case "BEND", "NDT":
		if strings.EqualFold(unit, "pass") {
			return value, "pass", nil
		}
		return 0, "", Invalid("弯曲或无损检测单位必须是 pass", unit)
	case "HARDNESS":
		if strings.EqualFold(unit, "HV") {
			return value, "HV", nil
		}
		return 0, "", Invalid("硬度试验单位必须是 HV", unit)
	default:
		if unit == "" {
			return 0, "", Invalid("试验单位不能为空", nil)
		}
		return value, unit, nil
	}
}

func divideExact(value Fixed, divisor int64, message string) (Fixed, string, error) {
	if int64(value)%divisor != 0 {
		return 0, "", Invalid(message, value.String())
	}
	return Fixed(int64(value) / divisor), "MPa", nil
}

func NormalizeLength(value Fixed, unit string) (Fixed, error) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "mm":
		return value, nil
	case "cm":
		return value * 10, nil
	case "m":
		return value * 1000, nil
	default:
		return 0, Invalid("长度单位必须是 mm、cm 或 m", unit)
	}
}

func NormalizeTemperature(value Fixed, unit string) (Fixed, error) {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "C", "°C":
		return value, nil
	case "F", "°F":
		return Fixed((int64(value) - 32000) * 5 / 9), nil
	case "K":
		return value - Fixed(273150), nil
	default:
		return 0, Invalid("温度单位必须是 C、F 或 K", unit)
	}
}

func NormalizeHeatInput(value Fixed, unit string) (Fixed, error) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "kj/mm":
		return value, nil
	case "j/mm":
		if int64(value)%1000 != 0 {
			return 0, Invalid("J/mm 换算结果超过三位小数", value.String())
		}
		return Fixed(int64(value) / 1000), nil
	case "kj/cm":
		if int64(value)%10 != 0 {
			return 0, Invalid("kJ/cm 换算结果超过三位小数", value.String())
		}
		return Fixed(int64(value) / 10), nil
	default:
		return 0, Invalid("热输入单位必须是 kJ/mm、J/mm 或 kJ/cm", unit)
	}
}
